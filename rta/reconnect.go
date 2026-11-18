package rta

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v7"
	"github.com/coder/websocket"
)

// wait blocks until any in-progress reconnect attempt has finished.
func (c *Conn) wait(ctx context.Context) error {
	c.reconnectMu.Lock()
	done := c.reconnectDone
	c.reconnectMu.Unlock()

	if done == nil {
		return nil
	}
	select {
	case <-done:
		return context.Cause(c.ctx) // nil unless the Conn was closed
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return context.Cause(c.ctx)
	}
}

// takeSubscriptionsForReconnect removes current subscriptions from the old
// WebSocket and returns only the subscriptions that should be restored on a new
// one. Subscriptions that are already unsubscribing are left for Unsubscribe to
// finish.
func (c *Conn) takeSubscriptionsForReconnect() (resubscribe []*Subscription) {
	c.subscriptionsMu.Lock()
	defer c.subscriptionsMu.Unlock()
	resubscribe = make([]*Subscription, 0, len(c.subscriptions))
	for _, subscription := range c.subscriptions {
		if subscription.shouldResubscribe() {
			resubscribe = append(resubscribe, subscription)
		}
	}
	clear(c.subscriptions)
	return resubscribe
}

// beginReconnect starts a reconnect gate if none is already active. It reports
// whether the caller owns running the reconnect.
func (c *Conn) beginReconnect() (chan struct{}, bool) {
	c.reconnectMu.Lock()
	defer c.reconnectMu.Unlock()
	return c.beginReconnectLocked()
}

// beginReconnectLocked is beginReconnect with reconnectMu held.
func (c *Conn) beginReconnectLocked() (chan struct{}, bool) {
	if c.ctx.Err() != nil {
		return nil, false
	}
	if c.reconnectDone != nil {
		return c.reconnectDone, false
	}
	done := make(chan struct{})
	c.reconnectDone = done
	return done, true
}

// finishReconnect clears and closes the reconnect gate for waiters. It instead
// keeps the gate and reports true when the socket the reconnect installed was
// lost meanwhile, because that loss found the gate held and started nothing.
func (c *Conn) finishReconnect(done chan struct{}) (again bool) {
	c.reconnectMu.Lock()
	if lost := c.lostConn; lost != nil && c.ctx.Err() == nil && c.isCurrentConn(lost) {
		// Keep the record until the next attempt replaces lost, so trackOn
		// still rejects subscriptions made on it meanwhile.
		c.reconnectMu.Unlock()
		return true
	}
	c.lostConn = nil
	if c.reconnectDone == done {
		c.reconnectDone = nil
	}
	c.reconnectMu.Unlock()
	close(done)
	return false
}

// connectionLost reconnects after conn failed, unless a reconnect already replaced it.
func (c *Conn) connectionLost(conn *websocket.Conn) {
	// Check under reconnectMu so a stale socket cannot overwrite a newer loss.
	c.reconnectMu.Lock()
	if !c.isCurrentConn(conn) {
		c.reconnectMu.Unlock()
		return
	}
	c.lostConn = conn
	// Claim the gate in the same section: a running reconnect sees lostConn
	// before it releases the gate, and a finished one cannot be followed by a
	// redundant reconnect for this report.
	done, ok := c.beginReconnectLocked()
	c.reconnectMu.Unlock()
	if ok {
		go c.runReconnect(done)
	}
}

// reconnect re-establishes the WebSocket connection. Only one reconnect may
// run at a time. Concurrent calls after the first are no-ops. It keeps retrying
// until the connection is back or the Conn is closed.
func (c *Conn) reconnect() {
	done, ok := c.beginReconnect()
	if !ok {
		return
	}
	c.runReconnect(done)
}

// runReconnect redials RTA and restores active subscriptions until reconnect
// succeeds, no subscriptions remain, or the Conn is closed. Neither a long
// outage nor a socket that keeps dropping mid-handshake ends the attempt: the
// subscriptions stay owned by the reconnect until it lands or the Conn closes.
func (c *Conn) runReconnect(done chan struct{}) {
	// rounds paces a service that keeps dropping sockets right after resubscribes succeed.
	rounds := c.dialer.backoff()
	for {
		c.restore()
		if !c.finishReconnect(done) {
			return
		}
		sleep := rounds.NextBackOff()
		c.log.Info("replacement WebSocket was lost while resubscribing", slog.Duration("sleep", sleep))
		timer := time.NewTimer(sleep)
		select {
		case <-timer.C:
		case <-c.ctx.Done():
			timer.Stop()
		}
	}
}

// restore runs one reconnect attempt for runReconnect.
func (c *Conn) restore() {
	c.log.Info("re-establishing WebSocket connection...")

	interruptedAttempts := 0
	// pending holds subscriptions interrupted on an earlier attempt. They stay
	// out of the ID map, where their old IDs could collide with new ones.
	var pending []*Subscription
	_, _ = backoff.Retry(c.ctx, func() (struct{}, error) {
		subscriptions := slices.Concat(pending, c.takeSubscriptionsForReconnect())
		pending = nil
		if len(subscriptions) == 0 {
			_ = c.closeWebSocket(websocket.StatusNormalClosure, "no active subscriptions")
			return struct{}{}, nil
		}
		conn, err := c.dialer.reconnect(c.ctx)
		if err != nil {
			// Only a closed Conn stops the dialer. Close has already run its
			// deactivation over the tracked set, so finish it for the ones we hold.
			c.deactivateAll(subscriptions)
			return struct{}{}, nil
		}
		// Publish under connMu with a ctx check so a dial that lands as Close
		// runs cannot slip in after Close swept c.conn: either Close sees this
		// socket, or this sees the cancelled ctx and closes it.
		c.connMu.Lock()
		if c.ctx.Err() != nil {
			c.connMu.Unlock()
			_ = conn.Close(websocket.StatusGoingAway, "connection closed")
			c.deactivateAll(subscriptions)
			return struct{}{}, nil
		}
		old := c.conn
		c.conn = conn
		c.connMu.Unlock()
		if old != nil {
			// Its reader may still be blocked; it returns once it sees it is not current.
			_ = old.CloseNow()
		}
		go c.read(conn)

		c.log.Info("resubscribing existing subscriptions...", slog.Int("count", len(subscriptions)))
		pending = c.resubscribe(conn, subscriptions)
		if len(pending) == 0 {
			return struct{}{}, nil
		}
		// Drop the socket so the next attempt cannot reuse one that went silent,
		// and record it first so trackOn rejects subscriptions made on it.
		c.connectionLost(conn)
		_ = conn.CloseNow()
		return struct{}{}, errConnectionInterrupted
	}, backoff.WithBackOff(c.dialer.backoff()), backoff.WithMaxElapsedTime(0), backoff.WithNotify(func(_ error, sleep time.Duration) {
		interruptedAttempts++
		c.log.Info("resubscribe interrupted; reconnecting again",
			slog.Int("attempt", interruptedAttempts), slog.Duration("sleep", sleep),
		)
	}))
	// Close may have run while a handshake was re-tracking a subscription or
	// while the retry loop was waiting. Finish deactivating anything it missed.
	if c.ctx.Err() != nil {
		c.deactivateAll(slices.Concat(pending, c.takeSubscriptionsForReconnect()))
	}
}

// deactivateAll reports the Conn's close cause to subscriptions the reconnect
// still held when the Conn closed underneath it.
func (c *Conn) deactivateAll(subscriptions []*Subscription) {
	cause := context.Cause(c.ctx)
	for _, subscription := range subscriptions {
		subscription.deactivate(cause)
	}
}

// resubscribe re-establishes all subscriptions inherited from the previous
// WebSocket connection on conn. Each re-subscribe attempt is bounded by the
// dialer's resubscribeTimeout (15 seconds by default).
// Terminal subscription failures are reported via [SubscriptionHandler.HandleError].
// It returns the untracked subscriptions that a lost or silent conn interrupted,
// for another reconnect attempt to retry.
func (c *Conn) resubscribe(conn *websocket.Conn, subscriptions []*Subscription) (interrupted []*Subscription) {
	var successCount atomic.Int32
	var interruptedMu sync.Mutex
	var wg sync.WaitGroup
	// One ping answers for every handshake that timed out in this pass.
	silent := sync.OnceValue(func() bool { return c.silent(conn) })
	for _, subscription := range subscriptions {
		wg.Go(func() {
			log := c.log.With(slog.Group("subscription",
				slog.Uint64("id", uint64(subscription.ID())),
				slog.String("resourceURI", subscription.ResourceURI()),
			))

			ctx, cancel := context.WithTimeout(c.ctx, c.dialer.resubscribeTimeout)
			defer cancel()
			subscription.opMu.Lock()
			if !subscription.shouldResubscribe() {
				// Unsubscribe removed it while the reconnect held it.
				subscription.opMu.Unlock()
				return
			}
			_, err := c.subscribe(ctx, subscription)
			subscription.opMu.Unlock()
			if err != nil {
				// A handshake timeout is a silent socket only if it also stops
				// answering pings; otherwise the service ignored this subscription.
				if err == errConnectionInterrupted || (err == context.DeadlineExceeded && c.ctx.Err() == nil && silent()) {
					interruptedMu.Lock()
					interrupted = append(interrupted, subscription)
					interruptedMu.Unlock()
					log.Error("resubscribe interrupted", slog.Any("error", err))
					return
				}
				subscription.deactivate(fmt.Errorf("resubscribe: %w", err))
				log.Error("error resubscribing", slog.Any("error", err))
				return
			}

			c.trackSubscription(subscription)
			successCount.Add(1)

			c.log.Debug("resubscribed", slog.Group("subscription",
				slog.Uint64("id", uint64(subscription.ID())),
				slog.String("custom", string(subscription.Custom())),
				slog.String("resourceURI", subscription.ResourceURI()),
			))
		})
	}

	wg.Wait()
	c.log.Info("resubscribed existing subscriptions",
		slog.Int("success", int(successCount.Load())),
		slog.Int("total", len(subscriptions)),
	)
	return interrupted
}

// silent reports whether conn fails to answer a ping within the ping timeout.
func (c *Conn) silent(conn *websocket.Conn) bool {
	return c.ping(conn) != nil
}

// reconnecting reports whether a reconnect holds the gate.
func (c *Conn) reconnecting() bool {
	c.reconnectMu.Lock()
	defer c.reconnectMu.Unlock()
	return c.reconnectDone != nil
}
