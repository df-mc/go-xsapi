package social

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/df-mc/go-xsapi/rta"
	"github.com/df-mc/go-xsapi/xal/xsts"
)

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

func (c *Client) do(ctx context.Context, method, u string, reqBody, respBody any, opts []internal.RequestOption) error {
	var r io.Reader
	if reqBody != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		defer buf.Reset()
		r = buf
	}

	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return fmt.Errorf("make request: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	internal.Apply(req, opts)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if respBody != nil {
			if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
				return fmt.Errorf("decode response body: %w", err)
			}
		}
		return nil
	case http.StatusNoContent:
		return nil
	default:
		return fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}

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
