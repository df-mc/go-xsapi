package xsapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/df-mc/go-xsapi/v2/mpsd"
	"github.com/df-mc/go-xsapi/v2/presence"
	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/df-mc/go-xsapi/v2/social"
	"github.com/df-mc/go-xsapi/v2/xal"
	"github.com/df-mc/go-xsapi/v2/xal/nsal"
	"github.com/df-mc/go-xsapi/v2/xal/xasd"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
	"golang.org/x/text/language"
)

// NewClient creates a new [Client] using a default [ClientConfig] and a
// 15-second timeout for the initial login. For more control over the
// configuration, use [ClientConfig.New] directly.
func NewClient(src TokenSource) (*Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	var c ClientConfig
	return c.New(ctx, src)
}

// New creates a new [Client] using the given [TokenSource] and [ClientConfig].
// The provided context governs the initial login, including
// requesting XSTS tokens and connecting to WebSocket services. NSAL title data
// is resolved lazily when an authenticated request first needs it.
//
// New clones the [ClientConfig.HTTPClient] internally so that the original
// client is never mutated. This means that passing [http.DefaultClient] or any
// shared [http.Client] is safe.
func (config ClientConfig) New(ctx context.Context, src TokenSource) (*Client, error) {
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	} else if _, ok := config.HTTPClient.Transport.(*Client); ok {
		panic("xsapi: Config.HTTPClient's underlying transport cannot be *Client")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	c := &Client{
		config: config,
		src:    src,
	}
	// Clone the HTTP client to avoid mutating the original, which is
	// particularly important when the caller passes http.DefaultClient
	// or any shared HTTP client.
	// The cloned client uses c itself as the transport so that all outgoing
	// requests are authenticated via RoundTrip.
	c.client = new(http.Client)
	*c.client = *config.HTTPClient
	c.client.Transport = c

	token, err := src.XSTSToken(ctx, internal.XBLRelyingParty)
	if err != nil {
		return nil, fmt.Errorf("request XSTS token: %w", err)
	}
	xui := token.UserInfo()
	if xui.XUID == "" {
		// XUID is only present on XSTS tokens scoped to 'http://xboxlive.com',
		// so [xsts.Token.Validate] alone is not sufficient to verify this.
		return nil, errors.New("xsapi: authorization token does not claim XUID")
	}
	c.userInfo = xui

	c.transport = &nsal.Transport{
		Base: c.baseTransport(),
		Resolver: nsal.NewResolver(nsalTokenSource{
			TokenSource:        src,
			authorizationToken: token,
		}),
	}

	// Connect to RTA services.
	c.rta, err = rta.Dial(ctx, c.HTTPClient(), c.Log())
	if err != nil {
		return nil, fmt.Errorf("dial RTA: %w", err)
	}

	// Initialise API clients, each scoped to their respective endpoint.
	c.mpsd = mpsd.New(c.HTTPClient(), c.RTA(), c.UserInfo(), c.Log().With("src", "mpsd"))
	c.social = social.New(c.HTTPClient(), c.RTA(), c.UserInfo(), c.Log().With("src", "social"))
	c.presence = presence.New(c.HTTPClient(), c.UserInfo())
	return c, nil
}

// TokenSource is the interface that supplies XSTS tokens and device tokens
// with the private key used to sign requests.
type TokenSource interface {
	xsts.TokenSource
	xasd.TokenSource
}

// nsalTokenSource adapts a Client token source for [nsal.Resolver]. It keeps
// the authorization token obtained during Client setup available for lazy NSAL
// title-data lookups.
type nsalTokenSource struct {
	TokenSource
	authorizationToken *xsts.Token
}

// XSTSToken returns the cached Xbox Live authorization token while it remains
// valid. Tokens for other relying parties, and expired authorization tokens,
// are delegated to the underlying TokenSource.
func (src nsalTokenSource) XSTSToken(ctx context.Context, relyingParty string) (*xsts.Token, error) {
	if relyingParty == internal.XBLRelyingParty && src.authorizationToken.Valid() {
		return src.authorizationToken, nil
	}
	return src.TokenSource.XSTSToken(ctx, relyingParty)
}

// ClientConfig holds the configuration for creating a [Client].
type ClientConfig struct {
	// HTTPClient is the HTTP client used to make requests. If nil,
	// [http.DefaultClient] is used. The client is cloned internally,
	// so the original is never mutated.
	HTTPClient *http.Client

	// Logger is the logger used by the client set and its underlying API
	// clients. If nil, [slog.Default] is used.
	Logger *slog.Logger

	// EnableChat enables the chat functionality.
	// EnableChat bool
}

// Client is a client set that aggregates API clients for each Xbox Live
// endpoint. It also implements [http.RoundTripper] to transparently
// authenticate outgoing requests with XSTS tokens and request signatures.
type Client struct {
	config ClientConfig
	client *http.Client
	src    TokenSource

	transport *nsal.Transport
	userInfo  xsts.UserInfo

	rta      *rta.Conn
	mpsd     *mpsd.Client
	social   *social.Client
	presence *presence.Client

	closeMu  sync.Mutex
	closed   atomic.Bool
	closeErr error
}

// HTTPClient returns the underlying HTTP client that automatically
// authenticates outgoing requests via [Client.RoundTrip].
func (c *Client) HTTPClient() *http.Client {
	return c.client
}

// RoundTrip implements [http.RoundTripper].
//
// RoundTrip always consumes the request body, even on error, as required by
// the [http.RoundTripper] contract.
func (c *Client) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBodyClosed bool
	if req.Body != nil {
		defer func() {
			if !reqBodyClosed {
				_ = req.Body.Close()
			}
		}()
	}
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	reqBodyClosed = true
	return c.transport.RoundTrip(req.WithContext(c.nsalContext(req.Context())))
}

// baseTransport returns the transport of the HTTP client passed via
// [ClientConfig.HTTPClient], or [http.DefaultTransport] if none was set.
func (c *Client) baseTransport() http.RoundTripper {
	if t := c.config.HTTPClient.Transport; t != nil {
		return t
	}
	return http.DefaultTransport
}

// TokenAndSignature resolves an XSTS token and signature policy for the given
// URL using NSAL (Network Security Allow List). Title-scoped NSAL data takes
// priority over default data when matching the URL to an endpoint.
//
// The returned signature policy may be used to sign a request via its Sign
// method. For most use cases, [Client.HTTPClient] handles this automatically.
//
// TokenAndSignature is intended for external endpoints that require the XSTS
// token to be embedded directly in the request body, such as PlayFab's
// /Client/LoginWithXbox endpoint.
func (c *Client) TokenAndSignature(ctx context.Context, u *url.URL) (_ *xsts.Token, policy nsal.SignaturePolicy, _ error) {
	if c.closed.Load() {
		return nil, policy, net.ErrClosed
	}
	token, policy, err := c.transport.TokenAndSignature(c.nsalContext(ctx), u)
	if err != nil {
		return nil, policy, err
	}
	xstsToken, ok := token.(*xsts.Token)
	if !ok {
		return nil, policy, fmt.Errorf("xsapi: unexpected NSAL token type %T", token)
	}
	return xstsToken, policy, nil
}

// WithoutAuthHeaders returns a cloned HTTP request configured to exclude
// specified authentication headers from being automatically added by
// [Client.HTTPClient].
//
// Header names are matched case-insensitively. If no headers are provided,
// both Authorization and Signature are excluded.
func WithoutAuthHeaders(req *http.Request, headers ...string) *http.Request {
	return nsal.WithoutAuthHeaders(req, headers...)
}

func (c *Client) nsalContext(ctx context.Context) context.Context {
	if client, ok := ctx.Value(xal.HTTPClient).(*http.Client); ok && client != nil {
		return ctx
	}
	if c.client == nil {
		return ctx
	}
	return context.WithValue(ctx, xal.HTTPClient, c.client)
}

// Log returns the [slog.Logger] configured via [ClientConfig.Logger].
func (c *Client) Log() *slog.Logger {
	return c.config.Logger
}

// TokenSource returns the [TokenSource] used by the client to supply XSTS
// tokens and the proof key for signing requests.
func (c *Client) TokenSource() TokenSource {
	return c.src
}

// MPSD returns the API client for the Xbox Live MPSD
// (Multiplayer Session Directory) API.
func (c *Client) MPSD() *mpsd.Client {
	return c.mpsd
}

// Social returns the API client for the Xbox Live Social APIs.
func (c *Client) Social() *social.Client {
	return c.social
}

// Presence returns the API client for the Xbox Live Presence API.
func (c *Client) Presence() *presence.Client {
	return c.presence
}

// RTA returns the connection to Xbox Live RTA (Real-Time Activity) services.
func (c *Client) RTA() *rta.Conn {
	return c.rta
}

// UserInfo returns the profile information for the caller, including their
// XUID, display name, and other metadata. It is derived from the XSTS token
// that relies on the party 'http://xboxlive.com' and is not updated during
// the lifecycle of the Client.
func (c *Client) UserInfo() xsts.UserInfo {
	return c.userInfo
}

// Close closes all underlying API clients with a 15-second timeout.
// For a context-aware variant, use [Client.CloseContext].
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return c.CloseContext(ctx)
}

// CloseContext closes all underlying API clients using the given context.
// Once closed, the Client cannot be reused as it also disconnects from
// WebSocket-based services such as RTA.
//
// CloseContext also removes the authenticated title's current Xbox Live
// presence via [presence.Client.Remove]. This is intentional: shutting down
// the client is treated as the title closing, so the presence is cleared
// immediately instead of waiting for it to expire on the server.
//
// Callers that want to release other resources without mutating presence
// should not call CloseContext.
func (c *Client) CloseContext(ctx context.Context) error {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()

	if c.closed.Load() {
		return c.closeErr
	}

	if err := errors.Join(
		c.mpsd.CloseContext(ctx),
		c.social.CloseContext(ctx),
		c.presence.CloseContext(ctx),
	); err != nil {
		return err
	}

	// Once rta is closed, the client is no longer usable and Close cannot be retried.
	c.closed.Store(true)
	if c.rta != nil {
		c.closeErr = c.rta.Close()
	}
	return c.closeErr
}

// AcceptLanguage returns a [internal.RequestOption] that appends the given
// language tags to the 'Accept-Language' header on outgoing requests,
// preserving any tags already present in the header.
func AcceptLanguage(tags []language.Tag) internal.RequestOption {
	return internal.AcceptLanguage(tags)
}

// RequestHeader returns a [internal.RequestOption] that sets a request header
// with the given name and value on outgoing requests.
func RequestHeader(key, value string) internal.RequestOption {
	return internal.RequestHeader(key, value)
}
