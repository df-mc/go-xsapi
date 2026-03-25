package sisu

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"sync"

	"github.com/df-mc/go-xsapi/xal/internal"
	"github.com/df-mc/go-xsapi/xal/nsal"
	"github.com/df-mc/go-xsapi/xal/xasd"
	"github.com/df-mc/go-xsapi/xal/xast"
	"github.com/df-mc/go-xsapi/xal/xasu"
	"github.com/df-mc/go-xsapi/xal/xsts"
	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

// New creates a new Session using src as the [oauth2.TokenSource] that provides Microsoft
// Account (MSA) access tokens. If sc is non nil, it will be used to customize session behavior,
// including resuming from a previous Session using a Snapshot.
func (conf Config) New(src oauth2.TokenSource, sc *SessionConfig) *Session {
	if sc == nil {
		sc = &SessionConfig{}
	}

	s := &Session{
		config: conf,
		msa:    src,
		device: sc.DeviceTokenSource,
		client: sc.HTTPClient,
	}
	if c := sc.Snapshot; c != nil {
		if sc.DeviceTokenSource == nil {
			panic("xal/sisu: DeviceTokenSource must be present in SessionConfig for re-using a Snapshot")
		}
		s.title = c.TitleToken
		s.user = c.UserToken
		s.xsts = c.XSTSTokens // will be filled if nil
	}
	if s.device == nil {
		s.device = xasd.ReuseTokenSource(conf.Config, nil, nil)
	}

	if s.client == nil {
		s.client = http.DefaultClient
	}
	if s.xsts == nil {
		s.xsts = make(map[string]*xsts.Token)
	}
	return s
}

// SessionConfig configures optional behavior when creating a Session.
type SessionConfig struct {
	// Snapshot contains previously issued tokens that may be reused.
	// When provided, the Session attempts to reuse contained tokens while
	// they remain valid.
	//
	// When reusing a Snapshot, the same DeviceTokenSource (and therefore
	// the same proof key) must also be provided. Otherwise, signed requests
	// will fail.
	Snapshot *Snapshot

	// DeviceTokenSource provides device tokens and exposes the private key
	// used to sign requests. If nil, a default token source is created from
	// [xasd.ReuseTokenSource].
	DeviceTokenSource xasd.TokenSource

	// HTTPClient is the HTTP client used to make requests.
	// If not present, [http.DefaultClient] will be used instead.
	HTTPClient *http.Client
}

// Snapshot contains restorable authentication state for a Session.
//
// A Snapshot may be persisted and later supplied to SessionConfig
// to resume a previous session without repeating the full SISU flow.
//
// The proof key is not stored in Snapshot. The caller must reuse the
// same DeviceTokenSource when restoring.
type Snapshot struct {
	// TitleToken is the XAST token used to authenticate the title.
	TitleToken *xast.Token

	// UserToken is the XASU token used to authenticate the user.
	UserToken *xasu.Token

	// XSTSTokens is a map whose keys are relying parties and whose values are the corresponding XSTS tokens.
	// Each XSTS token is scoped to a relying party. A relying party is typically a URI (for example, "http"
	// or "rp" URI) that identifies the Xbox Live service the token is intended for.
	XSTSTokens map[string]*xsts.Token
}

// Session implements an authentication session for Xbox Live using SISU endpoints.
//
// A Session coordinates the complete Xbox authentication chain:
// - XASD (Xbox Authentication Services for Devices)
// - XAST (Xbox Authentication Services for Titles)
// - XASU (Xbox Authentication Services for Users)
// - XSTS (Xbox Secure Token Service)
//
// Tokens are acquired lazily and cached in memory. Expired tokens
// are refreshed automatically on demand.
//
// A Session may be resumed by persisting the result of [Session.Snapshot] and
// providing it through SessionConfig when creating a new Session.
// When restoring, the same [xasd.TokenSource] (and proof key) must be reused
// to ensure request signatures remain valid.
//
// Session is safe for concurrent use.
type Session struct {
	// config is the configuration for the application used to authenticate with
	// Xbox Live services. Most of them are constants and specific to the title.
	config Config

	// msa is the [oauth2.TokenSource] that supplies user's Microsoft Account
	// access tokens to authenticate with XASU.
	msa oauth2.TokenSource
	// device is the [xasd.TokenSource] that provides the device token and the
	// proof key used for identifying a device used for authentication and signing
	// the requests.
	device xasd.TokenSource

	// title is the last XAST token acquired for authenticating a title.
	title *xast.Token
	// userToken is the last known XASU token acquired for authenticating
	// a user.
	user *xasu.Token
	// tokenMu guards title and user tokens from concurrent read-write access.
	// It must be held when token acquisition is happening to avoid duplicate
	// requests.
	tokenMu sync.Mutex

	// xsts is the map whose keys are relying parties and whose values are the
	// corresponding XSTS tokens.
	xsts map[string]*xsts.Token
	// xstsMu guards xsts tokens from concurrent read-write access.
	xstsMu sync.Mutex

	// resp is the last known response for SISU authorization request.
	// It contains title, user, and an XSTS token that relies on the
	// party "http://xboxlive.com" (aka. Authorization Token).
	resp *authorizationResponse
	// respMu guards SISU resp from concurrent read-write access.
	// It must be held while [authorize] is called to avoid duplicate
	// inflight requests.
	respMu sync.Mutex

	// client is the HTTP client used to make authentication requests.
	client *http.Client
}

// DeviceToken returns an XASD (Xbox Authentication Services for Device) token.
// The underlying token source is responsible for caching and refreshing the
// device token. Since device tokens are long-lived rate-limited, reusing a
// Snapshot together with the same DeviceTokenSource is recommended.
func (s *Session) DeviceToken(ctx context.Context) (*xasd.Token, error) {
	return s.device.DeviceToken(ctx)
}

// TitleToken returns an XAST (Xbox Authentication Services for Title) token
// obtained via the SISU flow.
//
// The token is cached and reused while valid. If expired or missing,
// a new authorization request is performed.
//
// THe provided context controls request cancellation and deadlines.
func (s *Session) TitleToken(ctx context.Context) (*xast.Token, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.title != nil && s.title.Valid() {
		return s.title, nil
	}

	resp, err := s.authorize(ctx)
	if err != nil {
		return nil, fmt.Errorf("sisu: authorize: %w", err)
	}
	// The title token in SISU authorization response has already been validated.
	s.title = resp.TitleToken
	return s.title, nil
}

// UserToken returns an XASU (Xbox Authentication Services for User) token
// obtained via the SISU flow.
//
// The token is cached and reused while valid. If expired or missing,
// a new authorization request is performed.
//
// The provided context controls request cancellation and deadlines.
func (s *Session) UserToken(ctx context.Context) (*xasu.Token, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.user != nil && s.user.Valid() {
		return s.user, nil
	}

	resp, err := s.authorize(ctx)
	if err != nil {
		return nil, fmt.Errorf("sisu: authorize: %w", err)
	}
	// The user token in SISU authorization response has already been validated.
	s.user = resp.UserToken
	return s.user, nil
}

// Snapshot returns a copy of the current authentication state.
//
// The returned Snapshot may be persisted and later supplied via
// SessionConfig to resume the session.
func (s *Session) Snapshot() *Snapshot {
	// Lock/unlock mutexes in correct order to avoid ABBA deadlocks.
	s.xstsMu.Lock()
	defer s.xstsMu.Unlock()

	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()

	return &Snapshot{
		TitleToken: s.title,
		UserToken:  s.user,
		XSTSTokens: maps.Clone(s.xsts),
	}
}

// XSTSToken requests an XSTS (Xbox Secure Token Service) token for
// the specified relying party.
//
// The relying party identifies the Xbox Live service for which the token
// is valid and determines the claims included in the token.
//
// XSTS tokens are cached per relying party and reused until expiration.
func (s *Session) XSTSToken(ctx context.Context, relyingParty string) (*xsts.Token, error) {
	s.xstsMu.Lock()
	defer s.xstsMu.Unlock()

	token, ok := s.xsts[relyingParty]
	if ok && token.Valid() {
		// Re-use the cached XSTS token as possible.
		return token, nil
	}
	var err error
	token, err = s.requestXSTS(ctx, relyingParty)
	if err != nil {
		return nil, err
	}
	if !token.Valid() {
		return nil, errors.New("xal/sisu: invalid XSTS token data")
	}
	s.xsts[relyingParty] = token
	return token, nil
}

// requestXSTS obtains a new XSTS token for the relying party.
//
// If relyingParty is the default relying party ("http://xboxlive.com"),
// it may be reused from cached SISU authorization response.
//
// Otherwise, it calls [xsts.Authorize] using the current XASD/XAST/XASU
// tokens, acquiring or refreshing them as necessary.
func (s *Session) requestXSTS(ctx context.Context, relyingParty string) (*xsts.Token, error) {
	if relyingParty == defaultRelyingParty {
		resp, err := s.authorize(ctx)
		if err != nil {
			return nil, fmt.Errorf("authorize: %w", err)
		}
		if resp.AuthorizationToken.Valid() {
			return resp.AuthorizationToken, nil
		}
	}

	device, err := s.DeviceToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("xal/sisu: request device token for XSTS token request: %w", err)
	}
	title, err := s.TitleToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("xal/sisu: request title token for XSTS token request: %w", err)
	}
	user, err := s.UserToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("xal/sisu: request user token for XSTS token request: %w", err)
	}

	return xsts.Authorize(ctx, s.config.Config, s.ProofKey(), relyingParty, []xsts.UnderlyingToken{
		device,
		title,
		user,
	})
}

// ProofKey returns the ECDSA private key as the proof key of the token. It is used to sign
// requests for various Xbox Live services.
func (s *Session) ProofKey() *ecdsa.PrivateKey {
	return s.device.ProofKey()
}

// authorize performs the SISU authorization request.
//
// It exchanges Microsoft Account (MSA) access token and XASD token with
// XAST/XASU and an XSTS token that relies on the party ("http://xboxlive.com").
//
// The response is cached while valid.
func (s *Session) authorize(ctx context.Context) (*authorizationResponse, error) {
	s.respMu.Lock()
	defer s.respMu.Unlock()

	if s.resp != nil && s.resp.Valid() {
		return s.resp, nil
	}

	device, err := s.DeviceToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("xal/sisu: request device token for authorization: %w", err)
	}
	token, err := s.msa.Token()
	if err != nil {
		return nil, fmt.Errorf("xal/sisu: request access token for authorization: %w", err)
	}

	td, err := nsal.Default(ctx)
	if err != nil {
		return nil, fmt.Errorf("xal/sisu: obtain NSAL default title endpoints: %w", err)
	}

	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(&authorizationRequest{
		AccessToken:       "t=" + token.AccessToken,
		ClientID:          s.config.ClientID,
		DeviceToken:       device.Token,
		Sandbox:           s.config.Sandbox,
		UseModernGamerTag: true,
		SiteName:          "user.auth.xboxlive.com",
		RelyingParty:      defaultRelyingParty,
		ProofKey:          internal.ProofKey(s.ProofKey()),
	}); err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}
	defer buf.Reset()
	requestURL := endpoint.JoinPath("authorize").String()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, buf)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("User-Agent", s.config.UserAgent)
	_, policy, ok := td.Match(req.URL)
	if !ok {
		return nil, fmt.Errorf("xal/sisu: NSAL title endpoint not found for %q", req.URL)
	}
	policy.Sign(req, buf.Bytes(), s.ProofKey())

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var r *authorizationResponse
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return nil, fmt.Errorf("decode response body: %w", err)
		}
		if !r.Valid() {
			return nil, errors.New("xal/sisu: invalid authorization response")
		}
		s.resp = r
		return r, nil
	default:
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}

// defaultRelyingParty is the default relying party desired on the Authorization Token present in
// SISU authorization response.
const defaultRelyingParty = "http://xboxlive.com"

// authorizationRequest describes the JSON payload used to authorize with SISU.
//
// The request must be signed using the proof key associated with the XASD token.
// The public key is embedded in this payload via the ProofKey field.
type authorizationRequest struct {
	// AccessToken is the OAuth2 access token for the user's Microsoft Account (MSA).
	//
	// The value is typically prefixed with "t=" or "d=" when sent to SISU.
	// It is typically obtained via the Authorization Code Flow (interactive login),
	// or the Device Authorization Flow (device code flow).
	//
	// The access token identifies the Microsoft Account being linked to Xbox
	// services and is exchanged for an XASU token.
	AccessToken string

	// ClientID is the OAuth2 Client ID associated with the title.
	// It must match the client ID used to obtain the Microsoft Account
	// access token.
	ClientID string `json:"AppId"`

	// DeviceToken is the XASD token associated with the user.
	// It represents the device initiating the authentication flow and is
	// bound to the proof key.
	DeviceToken string

	// ProofKey is the JSON Web Key representation of the ECDSA public
	// key corresponding to the device's private proof key.
	//
	// The corresponding private key must be used to sign the request. SISU
	// verifies the signature using this public key to ensure that request
	// originates from the device that owns the XASD token.
	ProofKey jose.JSONWebKey

	// RelyingParty specifies the party for which the returned Authorization Token
	// should be valid. It is typically "http://xboxlive.com". Additional XSTS tokens
	// targeting other relying parties must be requested separately via [xsts.Authorize].
	RelyingParty string

	// Sandbox is the sandbox ID in the configuration used for SISU authorization.
	// It is typically 'RETAIL' for most retail titles.
	Sandbox string

	// SessionID is the ID used for SISU authentication flow.
	// It is only used for devices that use SISU webviews, mainly Android and iOS devices.
	SessionID string `json:"SessionId,omitempty"`

	// SiteName is unknown, seemingly the site used for authorization.
	// It is always "user.auth.xboxlive.com".
	SiteName string `json:"SiteName,omitempty"`

	// UseModernGamerTag indicates whether the authentication flow should
	// use the modern gamertag when signing in. This should generally be set
	// to true for modern titles.
	UseModernGamerTag bool `json:"UseModernGamertag"`
}

// authorizationResponse represents the JSON payload returned by a
// successful JSON authorization request.
//
// A successful response contains XAST/XASU/XSTS tokens.
// These tokens together establish the authenticated Xbox Live
// session for the device, title, and user.
type authorizationResponse struct {
	// TitleToken is the XAST token bound to the title ID associated with
	// the specified OAuth2 ClientID used in the authorization request.
	TitleToken *xast.Token

	// UserToken is the XASU token that represented the authenticated
	// Microsoft Account (MSA) as an Xbox user identity. This token is
	// required when requesting XSTS tokens for Xbox Live services.
	UserToken *xasu.Token

	// AuthorizationToken is the XSTS token issued for the relying party
	// specified in the corresponding authorizationRequest.
	//
	// For SISU authorization, this is typically scoped to the default
	// relying party ("http://xboxlive.com"). Additional XSTS tokens for
	// other relying parties must be obtained separately via [xsts.Authorize].
	AuthorizationToken *xsts.Token

	// WebPage is the URL locating to the SISU's login page.
	// It may be a web page that indicates the user must complete additional
	// steps in a browser (for example, account creation or consent).
	WebPage string
}

// Valid returns whether the authorizationResponse is valid.
func (resp *authorizationResponse) Valid() bool {
	return resp != nil && resp.TitleToken.Valid() && resp.UserToken.Valid() && resp.AuthorizationToken.Valid()
}

// endpoint is the base URL for the SISU.
var endpoint = &url.URL{
	Scheme: "https",
	Host:   "sisu.xboxlive.com",
}
