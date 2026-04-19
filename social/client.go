package social

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

// unsubscriber captures just the part of the RTA connection needed during
// client shutdown.
//
// Client normally uses the live *rta.Conn injected by New. The interface exists
// so tests can simulate unsubscribe failures and verify that subscription state
// is preserved for retry instead of being discarded after a failed cleanup.
type unsubscriber interface {
	Unsubscribe(context.Context, *rta.Subscription) error
}

// subscriber captures the part of the RTA connection needed to create new
// subscriptions. Like [unsubscriber], the interface exists so tests can inject
// controlled behavior without constructing a real [rta.Conn].
type subscriber interface {
	Subscribe(context.Context, string) (*rta.Subscription, error)
}

// New returns a new [Client] using the provided components.
func New(client *http.Client, conn *rta.Conn, userInfo xsts.UserInfo, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		client:   client,
		sub:      conn,
		unsub:    conn,
		userInfo: userInfo,
		log:      log,
	}
}

// Client is an API client for Xbox Live Social APIs. It communicates
// with two endpoints available on Xbox Live:
//   - social.xboxlive.com for relationship management, such as adding or removing friends.
//   - peoplehub.xboxlive.com for querying user profiles.
type Client struct {
	client *http.Client
	sub    subscriber
	// closeMu serializes CloseContext so the closing gate cannot be reopened
	// by a concurrent caller before the active shutdown attempt finishes.
	closeMu sync.Mutex
	// unsub is the narrow shutdown dependency used for removing RTA
	// subscriptions. In production it is the same value as sub.
	// Keeping this separate allows tests to inject controlled failures for the
	// retry path without having to construct a real rta.Conn.
	unsub    unsubscriber
	userInfo xsts.UserInfo
	log      *slog.Logger

	subscriptionMu sync.RWMutex
	subscription   *rta.Subscription
	// subscribeDone is closed when an in-flight subscription fetch finishes.
	// It is nil when no fetch is currently running.
	subscribeDone        chan struct{}
	subscriptionHandlers []SubscriptionHandler
	// closing is set while [Client.CloseContext] is running to prevent new
	// background work from starting.
	closing atomic.Bool
	// subscriptionSeq is incremented on close to invalidate in-flight
	// background goroutines.
	subscriptionSeq atomic.Uint64
}

// Close closes the Client with a context of 15 seconds timeout.
//
// It unsubscribes from the RTA service if any subscription is present on the Client.
// In most cases, [github.com/df-mc/go-xsapi.Client.Close] should be preferred
// over calling this method directly.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return c.CloseContext(ctx)
}

// CloseContext closes the Client using the given context.
// Client is still usable after calling this method as it only resets the internal state.
//
// It unsubscribes from the RTA service if any subscription is present on the Client.
// In most cases, [github.com/df-mc/go-xsapi.Client.CloseContext] should be preferred
// over calling this method directly.
func (c *Client) CloseContext(ctx context.Context) error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()

	c.closing.Store(true)
	defer c.closing.Store(false)
	c.subscriptionSeq.Add(1)

	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	if c.subscribeDone != nil {
		close(c.subscribeDone)
		c.subscribeDone = nil
	}

	if c.subscription != nil {
		if err := c.unsub.Unsubscribe(ctx, c.subscription); err != nil {
			return fmt.Errorf("xsapi/social: unsubscribe RTA: %w", err)
		}
		c.subscription = nil
	}
	c.subscriptionHandlers = nil
	return nil
}
