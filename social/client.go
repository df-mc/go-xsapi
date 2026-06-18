package social

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

// New returns a new [Client] using the provided components.
func New(client *http.Client, conn *rta.Conn, userInfo xsts.UserInfo, log *slog.Logger) *Client {
	return NewWithRTA(client, internal.Subscriber(conn), internal.Unsubscriber(conn), userInfo, log)
}

// NewWithRTA returns a new [Client] using the provided components and RTA
// subscription transport.
func NewWithRTA(client *http.Client, subscriber RTASubscriber, unsubscriber RTAUnsubscriber, userInfo xsts.UserInfo, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	if subscriber == nil {
		subscriber = internal.Subscriber(nil)
	}
	if unsubscriber == nil {
		unsubscriber = internal.Unsubscriber(nil)
	}
	c := &Client{
		client:       client,
		subscriber:   subscriber,
		unsubscriber: unsubscriber,
		userInfo:     userInfo,
		log:          log,
	}
	c.subscription = rta.NewSubscription(socialEndpoint.JoinPath(
		"users",
		"xuid("+userInfo.XUID+")",
		"friends",
	).String(), &subscriptionHandler{
		Client: c,
		log:    c.log.With("src", "social subscription"),
	})
	return c
}

// RTASubscriber is the part of an RTA connection needed to create Social
// subscriptions.
type RTASubscriber interface {
	Subscribe(context.Context, *rta.Subscription) error
}

// RTAUnsubscriber is the part of an RTA connection needed to remove Social
// subscriptions.
type RTAUnsubscriber interface {
	Unsubscribe(context.Context, *rta.Subscription) error
}

// Client is an API client for Xbox Live Social APIs. It communicates
// with two endpoints available on Xbox Live:
//   - social.xboxlive.com for relationship management, such as adding or removing friends.
//   - peoplehub.xboxlive.com for querying user profiles.
type Client struct {
	client       *http.Client
	subscriber   RTASubscriber
	unsubscriber RTAUnsubscriber
	userInfo     xsts.UserInfo
	log          *slog.Logger

	subscriptionMu       sync.RWMutex
	subscription         *rta.Subscription
	subscriptionHandlers []SubscriptionHandler
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
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()

	if c.subscription.Active() {
		if err := c.unsubscriber.Unsubscribe(ctx, c.subscription); err != nil {
			return fmt.Errorf("xsapi/social: unsubscribe RTA: %w", err)
		}
	}
	c.subscriptionHandlers = nil
	return nil
}
