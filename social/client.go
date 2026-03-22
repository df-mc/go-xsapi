package social

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/df-mc/go-xsapi/rta"
	"github.com/df-mc/go-xsapi/xal/xsts"
)

// New returns a new [Client] using the provided components.
func New(client *http.Client, conn *rta.Conn, userInfo xsts.UserInfo, log *slog.Logger) *Client {
	return &Client{
		client:   client,
		rta:      conn,
		userInfo: userInfo,
		log:      log,
	}
}

// Client is an API client for Xbox Live Social APIs. It communicates
// with two endpoints available on Xbox Live:
//   - social.xboxlive.com for relationship management, such as adding or removing friends.
//   - peoplehub.xboxlive.com for querying user profiles.
type Client struct {
	client   *http.Client
	rta      *rta.Conn
	userInfo xsts.UserInfo
	log      *slog.Logger

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
// Client is still usable after calling this method as it only resets internal state.
// It unsubscribes from the RTA service if any subscription is present on the Client.
// In most cases, [github.com/df-mc/go-xsapi.Client.CloseContext] should be preferred
// over calling this method directly.
func (c *Client) CloseContext(ctx context.Context) (err error) {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()

	if c.subscription != nil {
		if err2 := c.rta.Unsubscribe(ctx, c.subscription); err2 != nil {
			err = errors.Join(err, fmt.Errorf("xsapi/social: unsubscribe RTA: %w", err2))
		}
	}
	return err
}
