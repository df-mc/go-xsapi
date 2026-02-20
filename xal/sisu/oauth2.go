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

	"github.com/df-mc/go-xsapi/xal/nsal"
	"github.com/df-mc/go-xsapi/xal/xasd"
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

func (conf Config) TokenSource(ctx context.Context, t *oauth2.Token) oauth2.TokenSource {
	tkr := &tokenRefresher{
		ctx:  ctx,
		conf: &conf,
	}
	if t != nil {
		tkr.refreshToken = t.RefreshToken
	}
	return oauth2.ReuseTokenSourceWithExpiry(t, tkr, time.Hour*3)
}

type tokenRefresher struct {
	ctx          context.Context
	conf         *Config
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

	req, err := http.NewRequestWithContext(tf.ctx, http.MethodPost, tf.conf.oauth2().Endpoint.TokenURL, strings.NewReader(url.Values{
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

	if tf.refreshToken != tk.RefreshToken {
		tf.refreshToken = tk.RefreshToken
	}
	return tk, nil
}

func (conf Config) AuthCodeURL(ctx context.Context, device xasd.TokenSource, state string, opts ...oauth2.AuthCodeOption) (string, error) {
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
		TitleID:     strconv.FormatInt(conf.TitleID, 10),
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
	nsal.AuthPolicy.Sign(req, buf.Bytes(), device.ProofKey())

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

func (conf Config) Exchange(ctx context.Context, code string, opts ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	return conf.oauth2().Exchange(ctx, code, append(opts,
		oauth2.SetAuthURLParam("scope", scope),
		oauth2.SetAuthURLParam("client_id", conf.ClientID),
	)...)
}

const scope = "service::user.auth.xboxlive.com::MBI_SSL"

func setDefaultParam(query map[string]string, key, value string) {
	if _, ok := query[key]; !ok {
		query[key] = value
	}
}

type authCodeRequest struct {
	ClientID    string `json:"AppId"`
	TitleID     string `json:"TitleId"`
	RedirectURI string `json:"RedirectUri"`
	DeviceToken string `json:"DeviceToken"`
	// Sandbox is always 'RETAIL'.
	Sandbox string
	// TokenType is always 'code'.
	TokenType string
	Scopes    []string `json:"Offers"`
	Query     map[string]string
}

type authCodeResponse struct {
	RedirectURL          string          `json:"MsaOauthRedirect"`
	MSARequestParameters json.RawMessage `json:"MsaRequestParameters"`
}
