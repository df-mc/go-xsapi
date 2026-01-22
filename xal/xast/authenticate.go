package xast

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/df-mc/go-xsapi/xal"
	"github.com/df-mc/go-xsapi/xal/internal"
	"github.com/df-mc/go-xsapi/xal/nsal"
	"github.com/df-mc/go-xsapi/xal/xasd"
	"github.com/go-jose/go-jose/v4"
)

func Authenticate(ctx context.Context, config xal.Config, token *xasd.Token, proofKey *ecdsa.PrivateKey) (*Token, error) {
	client := internal.ContextClient(ctx)
	defer client.CloseIdleConnections()

	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(request{
		RelyingParty: "http://auth.xboxlive.com",
		TokenType:    "JWT",
		Properties: properties{
			DeviceToken: token.Token,
			ProofKey: jose.JSONWebKey{
				Key: proofKey,
				Use: "sig",
			},
			TitleID: config.TitleID,
		},
	}); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://title.auth.xboxlive.com/title/authenticate", buf)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("User-Agent", config.UserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-xbl-contract-version", "1")
	nsal.AuthPolicy.Sign(req, buf.Bytes(), proofKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
	var t *Token
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode response body: %w", err)
	}
	if t == nil || !t.Valid() {
		return nil, errors.New("xast: invalid token result")
	}
	return t, nil
}

type (
	request    internal.TokenRequest[properties]
	properties struct {
		DeviceToken string
		ProofKey    jose.JSONWebKey
		TitleID     int64 `json:"TitleId"`
	}
)
