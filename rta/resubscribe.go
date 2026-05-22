package rta

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// resubscribe re-establishes all subscriptions inherited from the previous
// WebSocket connection. Each re-subscribe attempt has a timeout of 15 seconds.
// Failures are reported via [SubscriptionHandler.HandleReconnect].
func (c *Conn) resubscribe() []*Subscription {
	subscriptions := c.subscriptionsForReconnect()

	c.log.Info("reconnected, resubscribing existing subscriptions...", slog.Int("count", len(subscriptions)))

	successes := make([]*Subscription, 0, len(subscriptions))
	var successesMu sync.Mutex

	wg := new(sync.WaitGroup)
	wg.Add(len(subscriptions))
	for _, s := range subscriptions {
		go func(subscription *Subscription) {
			defer wg.Done()

			attempt := 0
			for {
				ctx, cancel := context.WithTimeout(c.ctx, time.Second*15)
				err := c.resubscribeDuringReconnect(ctx, subscription)
				cancel()
				if err == nil {
					successesMu.Lock()
					successes = append(successes, subscription)
					successesMu.Unlock()
					return
				}
				if errors.Is(err, errReconnectInterrupted) {
					return
				}
				if retryableSubscribeError(err) {
					delay := resubscribeBackoff(attempt)
					attempt++
					c.log.Warn("retrying RTA resubscribe after transient status",
						slog.Group("subscription",
							slog.Uint64("id", uint64(subscription.id())),
							slog.String("resourceURI", subscription.resourceURI),
						),
						slog.Any("error", err),
						slog.Duration("retry_after", delay),
					)
					if delay <= 0 {
						continue
					}
					readerDone := c.currentReaderDone()
					select {
					case <-time.After(delay):
						continue
					case <-readerDone:
						c.markReconnectNext()
						return
					case <-c.ctx.Done():
						return
					}
				}

				c.removeSubscription(subscription)
				go func(subscription *Subscription, err error) {
					c.notifyReconnect(subscription, err)
				}(subscription, err)
				c.log.Error("error resubscribing",
					slog.Group("subscription",
						slog.Uint64("id", uint64(subscription.id())),
						slog.String("resourceURI", subscription.resourceURI),
					),
					slog.Any("error", err),
				)
				return
			}
		}(s)
	}

	wg.Wait()
	c.log.Info("resubscribed existing subscriptions",
		slog.Int("attempted", len(subscriptions)),
		slog.Int("successful", len(successes)),
	)
	return successes
}

// retryableSubscribeError reports whether err is an RTA subscribe status that
// XSAPI treats as transient during resubscribe.
func retryableSubscribeError(err error) bool {
	var status *UnexpectedStatusError
	if !errors.As(err, &status) {
		return false
	}
	switch status.Code {
	case StatusThrottled, StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

// resubscribeBackoff returns XSAPI-style quadratic backoff capped at 60s.
func resubscribeBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Duration(attempt*attempt) * time.Second
	if delay > maxResubscribeBackoff {
		return maxResubscribeBackoff
	}
	return delay
}
