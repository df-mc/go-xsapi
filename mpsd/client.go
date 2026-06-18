package mpsd

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

// New returns a new [Client] using the provided components.
func New(client *http.Client, conn *rta.Conn, userInfo xsts.UserInfo, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	c := &Client{
		client:   client,
		rta:      conn,
		userInfo: userInfo,
		log:      log,

		sessions: make(map[string]*Session),
	}
	c.subscription = rta.NewSubscription(resourceURI, &subscriptionHandler{
		Client: c,
		log:    c.log.With("src", "subscription handler"),
	})
	return c
}

// Client is an API client for Xbox Live's MPSD (Multiplayer Session Directory) API.
type Client struct {
	client   *http.Client
	rta      *rta.Conn
	userInfo xsts.UserInfo
	log      *slog.Logger

	// subscription is the Real-Time Activity (RTA) subscription used to
	// receive notifications about changes to the session.
	subscription *rta.Subscription
	// subscriptionData is a custom payload included in the RTA subscription.
	// It contains the connection ID used to associate multiplayer sessions
	// created by the Client with the RTA subscription to receive changes to
	// themselves.
	subscriptionData atomic.Pointer[subscriptionData]

	sessions   map[string]*Session
	sessionsMu sync.RWMutex
}

// SessionByReference looks up for a multiplayer session identified by the reference.
// If one is found, SessionDescription will be returned, which contains metadata for
// the multiplayer session which can be used to participate the session in the future.
// An error is returned, if the [context.Context] exceeds a deadline, or if the API call
// was unsuccessful.
func (c *Client) SessionByReference(ctx context.Context, ref SessionReference, opts ...internal.RequestOption) (_ *SessionDescription, err error) {
	var d *SessionDescription
	if err := internal.Do(ctx, c.client, http.MethodGet, ref.URL().String(), nil, &d, append(opts,
		internal.ContractVersion(contractVersion),
	)); err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fmt.Errorf("mpsd: invalid session description received from %s", ref.URL())
	}
	return d, nil
}

// Close closes the Client with a context of 15 seconds timeout.
// It unsubscribes from the RTA service if any subscription is present on the Client.
// It is recommended to use the client-set's [github.com/df-mc/go-xsapi.Client.Close] method.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return c.CloseContext(ctx)
}

// CloseContext closes the Client with the [context.Context].
// It unsubscribes from the RTA service if any subscription is present on the Client.
// It is recommended to use the client-set's [github.com/df-mc/go-xsapi.Client.CloseContext] method.
func (c *Client) CloseContext(ctx context.Context) error {
	if c.rta == nil || c.subscription == nil {
		return nil
	}
	if err := c.rta.Unsubscribe(ctx, c.subscription); err != nil {
		return fmt.Errorf("mpsd: unsubscribe: %w", err)
	}
	return nil
}

// contractVersion is the value for the 'x-xbl-contract-version' request header.
// Request calls to MPSD endpoint should always contain this header.
const contractVersion = "107"
