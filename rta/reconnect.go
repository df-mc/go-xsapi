package rta

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

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

// popSubscriptions returns subscriptions that should be restored on a new
// WebSocket connection, plus all active subscriptions removed from the map.
// The full popped set is retained so reconnect failures can still notify
// subscriptions that are active but not worth resubscribing.
func (c *Conn) popSubscriptions() (resubscribe, popped []*Subscription) {
	c.subscriptionsMu.Lock()
	defer c.subscriptionsMu.Unlock()
	resubscribe = make([]*Subscription, 0, len(c.subscriptions))
	popped = make([]*Subscription, 0, len(c.subscriptions))
	for _, subscription := range c.subscriptions {
		if subscription.Active() {
			popped = append(popped, subscription)
		}
		if subscription.shouldResubscribe() {
			resubscribe = append(resubscribe, subscription)
		}
	}
	clear(c.subscriptions)
	return resubscribe, popped
}

// beginReconnect starts a reconnect gate if none is already active. It reports
// whether the caller owns running the reconnect.
func (c *Conn) beginReconnect() (chan struct{}, bool) {
	if c.ctx.Err() != nil {
		return nil, false
	}
	c.reconnectMu.Lock()
	defer c.reconnectMu.Unlock()
	if c.reconnectDone != nil {
		return c.reconnectDone, false
	}
	done := make(chan struct{})
	c.reconnectDone = done
	return done, true
}

// finishReconnect clears and closes the reconnect gate for waiters.
func (c *Conn) finishReconnect(done chan struct{}) {
	c.reconnectMu.Lock()
	if c.reconnectDone == done {
		c.reconnectDone = nil
	}
	c.reconnectMu.Unlock()
	close(done)
}

// startReconnect begins a background reconnect if one is not already running.
func (c *Conn) startReconnect() {
	done, ok := c.beginReconnect()
	if ok {
		go c.runReconnect(done)
	}
}

// reconnect re-establishes the WebSocket connection. Only one reconnect may
// run at a time. Concurrent calls after the first are no-ops. If establishment fails,
// the Conn is closed with the error as the cause.
func (c *Conn) reconnect() {
	done, ok := c.beginReconnect()
	if !ok {
		return
	}
	c.runReconnect(done)
}

// runReconnect redials RTA and restores active subscriptions until reconnect
// succeeds, no subscriptions remain, or the Conn must close.
func (c *Conn) runReconnect(done chan struct{}) {
	defer c.finishReconnect(done)

	c.log.Info("re-establishing WebSocket connection...")

	interruptedAttempts := 0
	for {
		subscriptions, popped := c.popSubscriptions()
		if len(subscriptions) == 0 {
			_ = c.closeWebSocket(websocket.StatusNormalClosure, "no active subscriptions")
			return
		}
		conn, err := c.dialer.dialWithBackoff(c.ctx)
		if err != nil {
			c.log.Error("error re-establishing WebSocket connection", slog.Any("error", err))
			for _, subscription := range popped {
				if subscription.Active() {
					c.trackSubscription(subscription)
				}
			}
			_ = c.close(fmt.Errorf("rta: reconnect: %w", err))
			return
		}
		c.connMu.Lock()
		c.conn = conn
		c.connMu.Unlock()
		go c.read(conn)

		c.log.Info("resubscribing existing subscriptions...", slog.Int("count", len(subscriptions)))
		if c.resubscribe(subscriptions) {
			interruptedAttempts++
			if interruptedAttempts >= maxResubscribeAttempts {
				err := fmt.Errorf("resubscribe interrupted after %d reconnect attempts", interruptedAttempts)
				c.log.Error("error re-establishing WebSocket connection", slog.Any("error", err))
				_ = c.close(fmt.Errorf("rta: reconnect: %w", err))
				return
			}
			_ = conn.Close(websocket.StatusGoingAway, "resubscribe interrupted")
			c.log.Info("resubscribe interrupted; reconnecting again")
			continue
		}
		return
	}
}

// maxResubscribeAttempts is the maximum number of interrupted resubscribe
// rounds before the Conn is closed.
const maxResubscribeAttempts = 4

// resubscribe re-establishes all subscriptions inherited from the previous
// WebSocket connection. Each re-subscribe attempt has a timeout of 15 seconds.
// Terminal subscription failures are reported via [SubscriptionHandler.HandleError].
// It reports whether any subscription was interrupted by a lost connection and
// should be retried by another reconnect attempt.
func (c *Conn) resubscribe(subscriptions []*Subscription) (interrupted bool) {
	var successCount atomic.Int32
	var interruptedSubscription atomic.Bool
	var wg sync.WaitGroup
	for _, subscription := range subscriptions {
		wg.Go(func() {
			log := c.log.With(slog.Group("subscription",
				slog.Uint64("id", uint64(subscription.ID())),
				slog.String("resourceURI", subscription.ResourceURI()),
			))

			ctx, cancel := context.WithTimeout(c.ctx, time.Second*15)
			defer cancel()
			subscription.opMu.Lock()
			err := c.subscribe(ctx, subscription)
			subscription.opMu.Unlock()
			if err != nil {
				if err == errConnectionInterrupted {
					c.trackSubscription(subscription)
					interruptedSubscription.Store(true)
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
	return interruptedSubscription.Load()
}
