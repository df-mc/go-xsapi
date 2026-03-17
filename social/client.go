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

// New makes a new API Client using the parameters.
func New(client *http.Client, conn *rta.Conn, userInfo xsts.UserInfo, log *slog.Logger) *Client {
	return &Client{
		client:   client,
		rta:      conn,
		userInfo: userInfo,
		log:      log,
	}
}

type Client struct {
	client   *http.Client
	rta      *rta.Conn
	userInfo xsts.UserInfo
	log      *slog.Logger

	subscriptionMu       sync.Mutex
	subscription         *rta.Subscription
	subscriptionHandlers []SubscriptionHandler

	once sync.Once
}

// Close closes the Client with a 15 seconds timeout.
// If the Client has made a subscription with RTA, the subscription will be unsubscribed.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return c.CloseContext(ctx)
}

func (c *Client) CloseContext(ctx context.Context) (err error) {
	c.once.Do(func() {
		c.subscriptionMu.Lock()
		defer c.subscriptionMu.Unlock()

		if c.subscription != nil {
			if err2 := c.rta.Unsubscribe(ctx, c.subscription); err2 != nil {
				err = errors.Join(err, fmt.Errorf("xsapi/social: unsubscribe RTA: %w", err2))
			}
		}
	})
	return err
}
