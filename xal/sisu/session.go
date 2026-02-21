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

// New creates a Session using the token source that supplies a token for the user's Microsoft
// Account. The provided session cache may be used to continue from the previous SISU session.
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

// SessionConfig encapsulates configuration for creating a SISU session.
type SessionConfig struct {
	// Snapshot is the latest state of the previous session.
	// If present, the Session will try to re-use the contained tokens.
	// Be sure to note that the same [xasd.DeviceTokenSource] should also
	// be present in SessionConfig in order to re-use the Snapshot since
	// it requires the same proof key to sign requests.
	Snapshot *Snapshot
	// DeviceTokenSource is the [xasd.TokenSource] which supplies a device
	// token along with its proof key used to authenticate with XASD (Xbox
	// Authentication Services for Devices). It is used for signing requests
	// using its proof key.
	DeviceTokenSource xasd.TokenSource
	// HTTPClient is the HTTP client used to make requests.
	// If not present, [http.DefaultClient] will be used instead.
	HTTPClient *http.Client
}

// A Snapshot contains the latest known state for a Session.
// Callers can store this struct in a safe place so they can
// continue from the previous Session when they're logging in
// to Xbox Live services again in the future.
type Snapshot struct {
	// TitleToken is the XAST (Xbox Authentication Services for
	// Titles) token used to authenticate the title.
	TitleToken *xast.Token
	// UserToken is the XASU (Xbox Authentication Services for
	// Users) token used to authenticate the user.
	UserToken *xasu.Token
	// XSTSTokens is a map whose keys are the relying parties
	// and values are the XSTS tokens that relies on the party.
	// Each token is scoped to a relying party, which determines
	// which services the token is valid for and which claims are included.
	XSTSTokens map[string]*xsts.Token
}

// Session implements a SISU session in Xbox Live.
//
// A Session efficiently requests each token for the following Xbox Authentication Services (XAS):
// - XASD (Xbox Authentication Services for Devices)
// - XAST (Xbox Authentication Services for Titles)
// - XASU (Xbox Authentication Services for Users)
// - XSTS (Xbox Secure Token Service)
//
// Callers can also re-use the previous Session by storing its Snapshot in a safe place
// and using them in [SessionConfig.Snapshot] again to continue from the previous session.
// TODO: Add more docs
type Session struct {
	config Config

	msa    oauth2.TokenSource
	device xasd.TokenSource

	title   *xast.Token
	user    *xasu.Token
	tokenMu sync.Mutex

	xsts   map[string]*xsts.Token
	xstsMu sync.Mutex

	resp   *authorizationResponse
	respMu sync.Mutex

	client *http.Client
}

// DeviceToken requests an XASD (Xbox Authentication Services for Device) token
// for the device specified in the Config using the ProofKey of Session. It re-uses
// the cached device token in the Session as possible. Device tokens are very long-living
// and issuing too many times in short criteria may result being rate-limited from XASD.
// That being said, it is recommended to use [Session.Snapshot] to cache the device token
// and re-using them in [Config.New].
func (s *Session) DeviceToken(ctx context.Context) (*xasd.Token, error) {
	return s.device.DeviceToken(ctx)
}

// TitleToken requests an XAST (Xbox Authentication Services for TitleData) token
// using the SISU authorization flow. It re-uses the cached title token in the
// Session as possible.
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

// UserToken requests an XASU (Xbox Authentication Services for User) token using the
// SISU authorization flow. It re-uses the cached user token in Session as possible.
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

// Snapshot returns the latest known state for the Session.
// Caller can store this in a safe place when they're done
// to continue when they're logging in again from the previous
// Session.
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

// XSTSToken requests an XSTS token that relies on the specific party.
// The relying-party is specifically a URI composed with either 'http'
// or 'rp' scheme, which indicates the endpoint that the token must be
// used for.
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

// requestXSTS requests an XSTS (Xbox Servicing Token Service) token that relies on the provided party.
// It uses the device, title, and user token for filling a request for the XSTS token.
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

// ProofKey returns the ECDSA private key as the proof key of the token, and also to sign
// requests for various Xbox Live services, and other related services like PlayFab or game-specific
// APIs like Minecraft.
func (s *Session) ProofKey() *ecdsa.PrivateKey {
	return s.device.ProofKey()
}

// authorize authorizes with SISU services to obtain title, user, or the authorization
// token (an XSTS token that relies on the party "http://xboxlive.com"). It uses the cache
// as possible.
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

// authorizationRequest describes the wire representation used to authorize with SISU services.
type authorizationRequest struct {
	// AccessToken is the OAuth2 access token for the user's Microsoft Account.
	// It may be obtained via WebView using SISU authentication flow, or device
	// code flow for some devices that doesn't support webviews natively, like
	// Nintendo Switch.
	AccessToken string
	// ClientID is the OAuth2 Client ID used to authenticate the AccessToken for the user's
	// Microsoft Account.
	ClientID string `json:"AppId"`
	// DeviceToken is the XASD (Xbox Authentication Services for Device) token.
	DeviceToken string
	// ProofKey is the private key used to sign requests.
	ProofKey jose.JSONWebKey
	// RelyingParty specifies the party that [authorizationResponse.AuthorizationToken]
	// should rely on. It is typically "http://xboxlive.com", and other XSTS tokens that
	// relies on other parties are requested manually by calling [xsts.Authorize].
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
	// UseModernGamerTag indicates whether to use modern gamertag to sign in to an Xbox Live account.
	UseModernGamerTag bool `json:"UseModernGamertag"`
}

// authorizationResponse describes the representation for payload format of a successful
// response body in SISU authorization request.
type authorizationResponse struct {
	// TitleToken is the XAST (Xbox Authentication Service for TitleData) token that belongs
	// to the title ID bound to the client ID used to authorize with SISU service.
	TitleToken *xast.Token
	// UserToken is the XASU (Xbox Authentication Service for User) token.
	UserToken *xasu.Token
	// AuthorizationToken is the XSTS token that relies on the party specified in the
	// RelyingParty field of an authorizationRequest.
	AuthorizationToken *xsts.Token
	// WebPage is the URL locating to a web page that shows the user is signed in to Xbox Live.
	// It may be a web page endorsing to create an Xbox Live account if none was bound to the
	// Microsoft Account of the user.
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
