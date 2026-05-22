package rta

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// reconnectState tracks the active reconnect/resubscribe wave.
type reconnectState struct {
	// next indicates the replacement socket dropped before the current reconnect
	// cycle finished, so a new dial should begin immediately after the current
	// resubscribe wave ends.
	next bool
	// done is closed when the reconnect dial/resubscribe wave is complete. It is
	// nil when no reconnect is in progress.
	done chan struct{}
	mu   sync.RWMutex
}

// triggerReconnect starts a reconnect if none is running, or signals the
// running reconnect to retry with a fresh dial once its current wave ends.
func (c *Conn) triggerReconnect() {
	if c.ctx.Err() != nil {
		return
	}
	var done chan struct{}
	c.reconnectState.mu.Lock()
	if c.reconnectState.done == nil {
		done = make(chan struct{})
		c.reconnectState.done = done
	} else {
		c.reconnectState.next = true
	}
	c.reconnectState.mu.Unlock()

	if done != nil {
		go c.reconnect(done)
	}
}

// reconnect re-establishes the WebSocket connection. Only one reconnect may
// run at a time.
func (c *Conn) reconnect(done chan struct{}) {
	if c.ctx.Err() != nil {
		c.finishReconnect(done)
		return
	}
	defer c.finishReconnect(done)

	c.log.Info("re-establishing WebSocket connection...")
	attempt := 0

	for {
		if readerDone := c.currentReaderDone(); readerDone != nil {
			select {
			case <-readerDone:
			case <-c.ctx.Done():
				return
			}
		}

		c.reconnectState.mu.Lock()
		c.reconnectState.next = false
		c.reconnectState.mu.Unlock()

		dialCtx, cancel := context.WithTimeout(c.ctx, reconnectDialTimeout)
		conn, err := c.dialer.dial(dialCtx)
		cancel()
		if err != nil {
			backoff := reconnectDialBackoff(attempt)
			attempt++
			c.log.Error("error re-establishing WebSocket connection",
				slog.Any("error", err),
				slog.Duration("retry_after", backoff),
			)
			if backoff <= 0 {
				continue
			}
			select {
			case <-time.After(backoff):
				continue
			case <-c.ctx.Done():
				return
			}
		}
		attempt = 0
		c.startReader(conn)

		successes := c.resubscribe()
		readerDone := c.currentReaderDone()
		if !c.reconnectWaveStable(readerDone) {
			continue
		}
		for _, subscription := range successes {
			go c.notifyReconnect(subscription, nil)
			c.log.Debug("resubscribed", slog.Group("subscription",
				slog.Uint64("id", uint64(subscription.id())),
				slog.String("custom", string(subscription.custom())),
				slog.String("resourceURI", subscription.resourceURI),
			))
		}
		return
	}
}

// finishReconnect closes done to unblock waiters and, if reconnectNext was
// set during the cycle, starts a new reconnect immediately.
func (c *Conn) finishReconnect(done chan struct{}) {
	currentDone := done
	var nextDone chan struct{}
	c.reconnectState.mu.Lock()
	if c.reconnectState.done == currentDone {
		c.reconnectState.done = nil
	}
	restart := c.ctx.Err() == nil && c.reconnectState.next
	if restart {
		c.reconnectState.next = false
		nextDone = make(chan struct{})
		c.reconnectState.done = nextDone
	}
	c.reconnectState.mu.Unlock()

	close(currentDone)

	if restart {
		go c.reconnect(nextDone)
	}
}

// reconnectWaveStable reports whether the reconnect wave still points at the
// same live reader channel and has not already been marked for another retry.
func (c *Conn) reconnectWaveStable(readerDone chan struct{}) bool {
	c.reconnectState.mu.RLock()
	next := c.reconnectState.next
	c.reconnectState.mu.RUnlock()
	if next {
		return false
	}
	if readerDone == nil {
		return true
	}
	if c.currentReaderDone() != readerDone {
		return false
	}
	select {
	case <-readerDone:
		return false
	default:
		return true
	}
}

// markReconnectNext asks the active reconnect wave to retry with a fresh dial.
func (c *Conn) markReconnectNext() {
	c.reconnectState.mu.Lock()
	c.reconnectState.next = true
	c.reconnectState.mu.Unlock()
}

// reconnectDialBackoff returns XSAPI-style quadratic reconnect backoff capped
// at 60s, with a small jitter to avoid clients reconnecting in lockstep.
func reconnectDialBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Duration(attempt*attempt) * time.Second
	if delay > reconnectBackoffMax {
		delay = reconnectBackoffMax
	}
	if reconnectBackoffJitterMax > 0 {
		delay += time.Duration(time.Now().UnixNano() % int64(reconnectBackoffJitterMax))
	}
	return delay
}
