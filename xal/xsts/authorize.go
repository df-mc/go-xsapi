package xsts

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
)

type TokenRequest internal.TokenRequest[TokenProperties]

func (r TokenRequest) Do(ctx context.Context, config xal.Config, proofKey *ecdsa.PrivateKey) (*Token, error) {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(r); err != nil {
		return nil, fmt.Errorf("encode token request: %w", err)
	}

	client := internal.ContextClient(ctx)
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://xsts.auth.xboxlive.com/xsts/authorize", buf)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("User-Agent", config.UserAgent)
	req.Header.Set("Content-Type", "application/json")
	nsal.AuthPolicy.Sign(req, buf.Bytes(), proofKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL, err)
	}
	var t *Token
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode response body: %w", err)
	}
	if t == nil || !t.Valid() {
		return nil, errors.New("xsts: invalid token result")
	}
	return t, nil
}

type TokenProperties struct {
	SandboxID             string   `json:"SandboxId,omitempty"`
	DeviceToken           string   `json:"DeviceToken,omitempty"`
	TitleToken            string   `json:"TitleToken,omitempty"`
	UserTokens            []string `json:"UserTokens,omitempty"`
	OptionalDisplayClaims []string `json:"OptionalDisplayClaims,omitempty"`
}
