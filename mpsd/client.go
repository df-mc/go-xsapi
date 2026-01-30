package mpsd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/df-mc/go-xsapi/rta"
	"github.com/df-mc/go-xsapi/xal/xsts"
)

func New(api API) *Client {
	return &Client{
		api:      api,
		sessions: make(map[string]*Session),
		closed:   make(chan struct{}),
	}
}

type Client struct {
	// api represents the underlying XSAPI Client used to create this Client.
	api API

	// subscription is the Real-Time Activity (RTA) subscription used to
	// receive notifications about changes to the session.
	subscription *rta.Subscription
	// subscriptionData is a custom payload included in the RTA subscription.
	// It contains the connection ID used to associate multiplayer sessions
	// created by the Client with the RTA subscription to receive changes to
	// themselves.
	subscriptionData *subscriptionData
	// subscriptionMu is a mutex that is held when either accessing subscription
	// and subscriptionData.
	subscriptionMu sync.Mutex

	sessions   map[string]*Session // TODO
	sessionsMu sync.Mutex          // TODO

	// closed is a channel that is closed when the Client is no longer usable.
	closed chan struct{}
	// once ensures that the closure of the Client occurs only once.
	once sync.Once
}

func (c *Client) SessionByReference(ctx context.Context, ref SessionReference) (_ *SessionDescription, err error) {
	var d *SessionDescription
	defer func() {
		if d == nil {
			err = fmt.Errorf("mpsd: invalid session description received from %s", ref.URL())
		}
	}()
	return d, c.do(ctx, http.MethodGet, ref.URL().String(), nil, d)
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
			if err2 := c.api.RTA().Unsubscribe(ctx, c.subscription); err2 != nil {
				err = errors.Join(err, fmt.Errorf("mpsd: unsubscribe: %w", err2))
			}
		}
		c.subscription, c.subscriptionData = nil, nil
	})
	return err
}

func (c *Client) do(ctx context.Context, method, url string, reqBody, respBody any) error {
	var r io.Reader
	if reqBody != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		defer buf.Reset()
		r = buf
	}

	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if respBody != nil {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("X-Xbl-Contract-Version", contractVersion)

	resp, err := c.api.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if respBody != nil {
			if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
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

type API interface {
	rta.Provider
	xsts.UserInfoProvider
	internal.HTTPClient
	internal.Logger
}

type Provider interface {
	MPSD() *Client
}

const contractVersion = "107"
