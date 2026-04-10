package sisu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/df-mc/go-xsapi/v2/xal/internal/timestamp"
	"github.com/df-mc/go-xsapi/v2/xal/nsal"
	"github.com/df-mc/go-xsapi/v2/xal/xasd"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/microsoft"
)

// DeviceAuth returns a device auth struct which contains a device code
// and authorization information provided for users to enter on another device.
func (conf Config) DeviceAuth(ctx context.Context) (*oauth2.DeviceAuthResponse, error) {
	return conf.oauth2().DeviceAuth(ctx, oauth2.SetAuthURLParam("response_type", "device_code"))
}

// DeviceAccessToken continuously polls the access token in the device authentication flow.
// If the [context.Context] has exceeded its deadline, it will return a nil *oauth2.Token with
// the contextual error.
func (conf Config) DeviceAccessToken(ctx context.Context, da *oauth2.DeviceAuthResponse) (*oauth2.Token, error) {
	return conf.oauth2().DeviceAccessToken(ctx, da)
}

// oauth2 returns an [oauth2.Config] that may be used for exchanging access tokens
// or starting a device code authentication flow using Windows Live tokens.
func (conf Config) oauth2() *oauth2.Config {
	endpoint := microsoft.LiveConnectEndpoint
	endpoint.DeviceAuthURL = "https://login.live.com/oauth20_connect.srf"

	return &oauth2.Config{
		Endpoint: endpoint,
		ClientID: conf.ClientID,
		Scopes:   []string{scope},
		// RedirectURI is not set here to prevent appearing as 'redirect_uri' query in the URL.
	}
}

// TokenSource returns a [oauth2.TokenSource] that returns t until t expires,
// automatically refreshing it as necessary using the provided context.
func (conf Config) TokenSource(ctx context.Context, t *oauth2.Token) oauth2.TokenSource {
	tkr := &tokenRefresher{
		ctx:  ctx,
		conf: &conf,
	}
	if t != nil {
		tkr.refreshToken = t.RefreshToken
	}
	return oauth2.ReuseTokenSource(t, tkr)
}

// A tokenRefresher continuously refreshes OAuth2 token from the Windows Live endpoint.
// It is not safe for concurrent access and thus needs to be wrapped by [oauth2.ReuseTokenSource].
type tokenRefresher struct {
	// ctx should be used as the bag of properties.
	// It is not used for controlling the deadline.
	ctx context.Context
	// conf is the underlying Config used to make refresh requests.
	conf *Config
	// refreshToken is the last known refresh token.
	refreshToken string
}

// Token refreshes the [oauth2.Token] using the refresh token
// retrieved  from previous token.
// WARNING: Token is not safe for concurrent access, as it
// updates the tokenRefresher's refreshToken field.
// Within this package, it is used by reuseTokenSource which
// synchronizes calls to this method with its own mutex.
func (tf *tokenRefresher) Token() (*oauth2.Token, error) {
	if tf.refreshToken == "" {
		return nil, errors.New("xal/sisu: token expired and refresh token is not set")
	}

	// tf.ctx should be used as the bag of properties here.
	// It shouldn't control any deadlines for inflight requests.
	req, err := http.NewRequestWithContext(context.WithoutCancel(tf.ctx), http.MethodPost, tf.conf.oauth2().Endpoint.TokenURL, strings.NewReader(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tf.refreshToken},
		"scope":         {scope},
		"client_id":     {tf.conf.ClientID},
	}.Encode()))
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", tf.conf.UserAgent)

	var client *http.Client
	if hc, ok := tf.ctx.Value(oauth2.HTTPClient).(*http.Client); ok {
		client = hc
	} else {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("POST %s: %s", tf.conf.oauth2().Endpoint.TokenURL, resp.Status)
	}
	var tk *oauth2.Token
	if err := json.NewDecoder(resp.Body).Decode(&tk); err != nil {
		return nil, fmt.Errorf("decode response body: %w", err)
	}
	if tk == nil || !tk.Valid() {
		return nil, errors.New("xal/sisu: invalid oauth2 token")
	}
	if tk.Expiry.IsZero() {
		// OAuth2 tokens obtained via the "refresh_token" grant only include
		// the "expires_in" field and do not provide explicit expiration time.
		// Therefore, we need to estimate and set the "expiry" field ourselves.
		//
		// The [oauth2.Token.expired] method always return false when the
		// Expiry field is zero. If we do not set it here, the token would continue
		// to be used even after it has actually expired.
		tk.Expiry = time.Now().Add(time.Second * time.Duration(tk.ExpiresIn))
	}

	if tf.refreshToken != tk.RefreshToken {
		tf.refreshToken = tk.RefreshToken
	}
	return tk, nil
}

// AuthCodeURL returns a URL to Microsoft's title-themed page that asks
// the user to log in to their Microsoft Account.
// It is similar to [oauth2.Config.AuthCodeURL], but requests the URL using
// a special endpoint. It also reports an error unlike the original method.
//
// The device token source is used to identify the device used for login
// and to sign the request using its proof key. Once the user's [oauth2.Token]
// has retrieved, it should be also passed to [Config.New] via [SessionConfig.Device]
// to sign in to Xbox Live with the same device.
//
// From [oauth2.Config.AuthCodeURL]:
// State is an opaque value used by the client to maintain state between the
// request and callback. The authorization server includes this value when
// redirecting the user agent back to the client.
//
// Opts may include [AccessTypeOnline] or [AccessTypeOffline], as well
// as [ApprovalForce].
//
// To protect against CSRF attacks, opts should include a PKCE challenge
// (S256ChallengeOption). Not all servers support PKCE. An alternative is to
// generate a random state parameter and verify it after exchange.
// See https://datatracker.ietf.org/doc/html/rfc6749#section-10.12 (predating
// PKCE), https://www.oauth.com/oauth2-servers/pkce/ and
// https://www.ietf.org/archive/id/draft-ietf-oauth-v2-1-09.html#name-cross-site-request-forgery (describing both approaches)
func (conf Config) AuthCodeURL(ctx context.Context, device xasd.TokenSource, state string, opts ...oauth2.AuthCodeOption) (string, error) {
	if conf.TitleID == 0 {
		return "", errors.New("xal/sisu: Config.TitleID must be present for requesting Authorization Code Flow")
	}

	dt, err := device.DeviceToken(ctx)
	if err != nil {
		return "", fmt.Errorf("request device token: %w", err)
	}

	u, err := url.Parse(conf.oauth2().AuthCodeURL(state, opts...))
	if err != nil {
		return "", fmt.Errorf("parse auth code URL: %w", err)
	}
	q := u.Query()

	reqBody := &authCodeRequest{
		ClientID:    conf.ClientID,
		TitleID:     json.Number(strconv.FormatInt(conf.TitleID, 10)),
		RedirectURI: conf.RedirectURI,
		DeviceToken: dt.Token,
		Sandbox:     conf.Sandbox,
		TokenType:   "code",
		Scopes:      []string{scope},
		Query:       make(map[string]string),
	}
	for k, v := range q {
		switch k {
		case "redirect_uri", "scope":
			continue
		}
		if len(v) != 1 {
			return "", fmt.Errorf("xal/sisu: URL query %q cannot be specified more than once", k)
		}
		reqBody.Query[k] = v[0]
	}
	setDefaultParam(reqBody.Query, "display", "android_phone")
	setDefaultParam(reqBody.Query, "prompt", "select_account")

	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		return "", fmt.Errorf("encode request body: %w", err)
	}
	defer buf.Reset()

	requestURL := endpoint.JoinPath("authenticate").String()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, buf)
	if err != nil {
		return "", fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("User-Agent", conf.UserAgent)
	req.Header.Set("x-xbl-contract-version", "1")
	nsal.AuthPolicy.Sign(req, buf.Bytes(), device.ProofKey(), timestamp.Now())

	var client *http.Client
	if hc, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok {
		client = hc
	} else {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	timestamp.Update(resp.Header)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
	var respBody *authCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return "", fmt.Errorf("decode response body: %w", err)
	}
	if respBody == nil || respBody.RedirectURL == "" {
		return "", errors.New("xal/sisu: invalid authenticate response body")
	}
	return respBody.RedirectURL, nil
}

// Exchange exchanges an [oauth2.Token] by using an authorization code.
//
// It is used after the login page redirects the user back to the RedirectURI.
//
// The provided context optionally controls which HTTP client is used.
//
// The code will be in the [http.Request.FormValue]("code"). Before
// calling Exchange, be sure to validate [http.Request.FormValue]("state") if you are
// using it to protect against CSRF attacks.
//
// If using PKCE to protect against CSRF attacks, opts should include a
// VerifierOption.
func (conf Config) Exchange(ctx context.Context, code string, opts ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	return conf.oauth2().Exchange(ctx, code, append(opts,
		oauth2.SetAuthURLParam("scope", scope),
		oauth2.SetAuthURLParam("client_id", conf.ClientID),
	)...)
}

// scope is the default scope used for requesting [oauth2.Token]
// for using to request an XASU (Xbox Authentication for Services)
// token.
const scope = "service::user.auth.xboxlive.com::MBI_SSL"

// setDefaultParam conditionally sets a value for the key if not present in query.
func setDefaultParam(query map[string]string, key, value string) {
	if _, ok := query[key]; !ok {
		query[key] = value
	}
}

type (
	// authCodeRequest represents a wire JSON data used to request
	// Authorization Code Flow using SISU endpoints.
	authCodeRequest struct {
		// ClientID is the ID for the application.
		// It is specific to title.
		// Typically derived from [Config.ClientID].
		ClientID string `json:"AppId"`
		// TitleID is the numeric identifier for the title in string form.
		// It should be derived from [Config.TitleID].
		TitleID json.Number `json:"TitleId"`
		// RedirectURI is the redirect URI bound to the client ID.
		// It should be derived from [Config.RedirectURI].
		RedirectURI string `json:"RedirectUri"`
		// DeviceToken is the XASD token used to identify the device
		// used for login.
		DeviceToken string `json:"DeviceToken"`
		// Sandbox is always 'RETAIL'.
		Sandbox string `json:"Sandbox"`
		// TokenType is always 'code'.
		TokenType string `json:"TokenType"`
		// Scopes is a list of scopes desired to be granted in the [oauth2.Token]
		// to be requested using the Authorization Code Flow.
		Scopes []string `json:"Offers"`
		// Query is the additional query parameters that should be passed to the resulting URL.
		Query map[string]string `json:"Query"`
	}
	// authCodeResponse represents the on-wire format of the JSON response.
	authCodeResponse struct {
		// RedirectURL is the URL to Microsoft's login page that finally redirects
		// to the RedirectURI specific to the client ID.
		RedirectURL string `json:"MsaOauthRedirect"`
		// MSARequestParameters json.RawMessage `json:"MsaRequestParameters"`
	}
)
