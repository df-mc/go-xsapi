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

	"github.com/df-mc/go-xsapi/xal/internal"
	"github.com/df-mc/go-xsapi/xal/nsal"
	"github.com/df-mc/go-xsapi/xal/xasd"
	"golang.org/x/oauth2"
)

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
	req, err := http.NewRequest(http.MethodPost, requestURL, buf)
	if err != nil {
		return "", fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("User-Agent", conf.UserAgent)
	req.Header.Set("x-xbl-contract-version", "1")
	nsal.AuthPolicy.Sign(req, buf.Bytes(), device.ProofKey())

	resp, err := internal.ContextClient(ctx).Do(req)
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
