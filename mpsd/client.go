package mpsd

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
	"sync/atomic"
	"time"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/df-mc/go-xsapi/rta"
	"github.com/df-mc/go-xsapi/xal/xast"
	"github.com/df-mc/go-xsapi/xal/xsts"
)

func New(client *http.Client, conn *rta.Conn, userInfo xsts.UserInfo, titleInfo xast.TitleInfo, log *slog.Logger) *Client {
	return &Client{
		client:    client,
		rta:       conn,
		userInfo:  userInfo,
		titleInfo: titleInfo,
		log:       log,

		sessions: make(map[string]*Session),
	}
}

type Client struct {
	client    *http.Client
	rta       *rta.Conn
	userInfo  xsts.UserInfo
	titleInfo xast.TitleInfo
	log       *slog.Logger

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

	// once ensures that the closure of the Client occurs only once.
	once sync.Once
}

// SessionByReference looks up for a multiplayer session identified by the reference.
// If one is found, SessionDescription will be returned, which contains metadata for
// the multiplayer session which can be used to participate the session in the future.
// An error is returned, if the [context.Context] exceeds a deadline, or if the API call
// was unsuccessful.
func (c *Client) SessionByReference(ctx context.Context, ref SessionReference) (_ *SessionDescription, err error) {
	var d *SessionDescription
	if err := c.do(ctx, http.MethodGet, ref.URL().String(), nil, &d); err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fmt.Errorf("mpsd: invalid session description received from %s", ref.URL())
	}
	return d, nil
}

// Close closes the Client with a context of 15 seconds timeout.
// It unsubscribes from the RTA service if any subscription is present on the Client.
// Although Close can be called many times, it is recommended to use the client-set's
// [github.com/df-mc/xsapi-go.Client.Close] method.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return c.CloseContext(ctx)
}

// CloseContext closes the Client with the [context.Context].
// It unsubscribes from the RTA service if any subscription is present on the Client.
// Although CloseContext can be called many times, it is recommended to use the client-set's
// [github.com/df-mc/xsapi-go.Client.CloseContext] method.
func (c *Client) CloseContext(ctx context.Context) (err error) {
	c.once.Do(func() {
		c.subscriptionMu.Lock()
		defer c.subscriptionMu.Unlock()

		if c.subscription != nil {
			if err2 := c.rta.Unsubscribe(ctx, c.subscription); err2 != nil {
				err = errors.Join(err, fmt.Errorf("mpsd: unsubscribe: %w", err2))
			}
		}
		c.subscription, c.subscriptionData = nil, nil
	})
	return err
}

// do is a tiny wrapper around HTTP requests for API calls to the session directory.
// If reqBody is non-nil, it will be JSON-encoded then used as the request body.
// If respBody is non-nil, it will be JSON-decoded from the response body.
// The [context.Context] is used for making a request call using the underlying HTTP client.
func (c *Client) do(ctx context.Context, method, url string, reqBody, respBody any) error {
	var r io.Reader
	if reqBody != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		defer buf.Reset()
		r = buf
		fmt.Println(buf.String())
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
	if etag, ok := ctx.Value(internal.ETag).(*atomic.Pointer[string]); ok {
		if s := etag.Load(); s != nil {
			req.Header.Set("If-None-Match", *s)
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if etag := resp.Header.Get("ETag"); etag != "" {
			if ptr, ok := ctx.Value(internal.ETag).(*atomic.Pointer[string]); ok {
				ptr.Store(&etag)
			}
		}
		if respBody != nil {
			if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
				return fmt.Errorf("decode response body: %w", err)
			}
		}
		return nil
	case http.StatusNotModified, http.StatusNoContent:
		return nil
	default:
		return fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}

// contractVersion is the value for the 'x-xbl-contract-version' request header.
// Request calls to MPSD endpoint should always contain this header.
const contractVersion = "107"
