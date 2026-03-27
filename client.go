package xsapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/df-mc/go-xsapi/mpsd"
	"github.com/df-mc/go-xsapi/presence"
	"github.com/df-mc/go-xsapi/rta"
	"github.com/df-mc/go-xsapi/social"
	"github.com/df-mc/go-xsapi/xal/nsal"
	"github.com/df-mc/go-xsapi/xal/xasd"
	"github.com/df-mc/go-xsapi/xal/xsts"
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
// requesting XSTS tokens, fetching NSAL data, and connecting to WebSocket services.
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

	// NSAL (Network Security Allow List) provides two kinds of title data:
	//  - Default data covering *.xboxlive.com endpoints
	//  - Title-scoped data for title-specific endpoints such as PlayFab
	//
	// Title-scoped data takes priority over default data when resolving the
	// relying party for an XSTS token. Default data is used as the fallback.
	c.defaultTitle, err = nsal.Default(ctx)
	if err != nil {
		return nil, fmt.Errorf("request NSAL default title data: %w", err)
	}
	c.currentTitle, err = nsal.Current(ctx, token, src.ProofKey())
	if err != nil {
		return nil, fmt.Errorf("request NSAL title data for current authenticated title: %w", err)
	}

	// Connect to RTA services.
	c.rta, err = config.RTADialer.DialContext(ctx, c.HTTPClient())
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

// ClientConfig holds the configuration for creating a [Client].
type ClientConfig struct {
	// HTTPClient is the HTTP client used to make requests. If nil,
	// [http.DefaultClient] is used. The client is cloned internally,
	// so the original is never mutated.
	HTTPClient *http.Client

	// Logger is the logger used by the client set and its underlying API
	// clients. If nil, [slog.Default] is used.
	Logger *slog.Logger

	// RTADialer is used to establish a connection to Xbox Live RTA
	// (Real-Time Activity) services.
	RTADialer rta.Dialer

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

	defaultTitle, currentTitle *nsal.TitleData
	userInfo                   xsts.UserInfo

	rta      *rta.Conn
	mpsd     *mpsd.Client
	social   *social.Client
	presence *presence.Client

	once sync.Once
}

// HTTPClient returns the underlying HTTP client that automatically
// authenticates outgoing requests via [Client.RoundTrip].
func (c *Client) HTTPClient() *http.Client {
	return c.client
}

// RoundTrip implements [http.RoundTripper]. It resolves an XSTS token and
// signature policy for the request URL using NSAL (Network Security Allow List),
// then sets the 'Authorization' and 'Signature' headers before forwarding the request to
// the underlying transport.
//
// RoundTrip always consumes the request body, even on error, as required by
// the [http.RoundTripper] contract. The request is cloned before any headers
// are set to avoid mutating the original.
func (c *Client) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		// The [http.RoundTripper] contract requires the body to be closed
		// by the caller of RoundTrip, even on error. We handle it here
		// rather than delegating to the base transport because the body
		// is buffered for signing before being forwarded.
		defer req.Body.Close()
	}

	// Propagate the request's context so that XSTS token retrieval
	// respects any deadlines or cancellations set by the caller.
	ctx := req.Context()
	token, policy, err := c.TokenAndSignature(ctx, req.URL)
	if err != nil {
		return nil, fmt.Errorf("request XSTS token and signature: %w", err)
	}

	var (
		// Clone the request so that the original headers are never mutated,
		// as required by the [http.RoundTripper] contract.
		req2 = req.Clone(ctx)
		// Body bytes buffered for inclusion in the request signature.
		data []byte
	)
	token.SetAuthHeader(req2)

	// If a body is present, it is buffered in full so that it can be included
	// in the 'Signature' header. It is then restored on the cloned request.
	if req.Body != nil {
		signingBuffer := &bytes.Buffer{}
		if _, err := signingBuffer.ReadFrom(req.Body); err != nil {
			signingBuffer.Reset()
			return nil, fmt.Errorf("clone request body: %w", err)
		}
		data, req2.Body = signingBuffer.Bytes(), io.NopCloser(signingBuffer)
	}
	policy.Sign(req2, data, c.src.ProofKey(), time.Now())

	return c.baseTransport().RoundTrip(req2)
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
	// Title-scoped data is checked first. Default data is only consulted as a
	// fallback because it can contain duplicate entries for the same endpoint
	// (e.g. *.playfabapi.com may appear in both title-scoped and default data).
	endpoint, policy, ok := c.currentTitle.Match(u)
	if !ok {
		endpoint, policy, ok = c.defaultTitle.Match(u)
		if !ok {
			return nil, policy, fmt.Errorf("no endpoint was found for %s", u)
		}
	}

	token, err := c.src.XSTSToken(ctx, endpoint.RelyingParty)
	if err != nil {
		return nil, policy, fmt.Errorf("request XSTS token: %w", err)
	}
	return token, policy, nil
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
func (c *Client) CloseContext(ctx context.Context) (err error) {
	c.once.Do(func() {
		err = errors.Join(
			c.mpsd.CloseContext(ctx),
			c.social.CloseContext(ctx),
			c.presence.CloseContext(ctx),

			c.rta.Close(),
		)
	})
	return err
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
