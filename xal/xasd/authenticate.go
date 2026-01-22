package xasd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"github.com/df-mc/go-xsapi/xal"
	"github.com/df-mc/go-xsapi/xal/internal"
	"github.com/df-mc/go-xsapi/xal/nsal"
	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
)

// Authenticate requests a Token representing the device in Xbox Live.
// The proof key is used to sign the ongoing request and must be the
// same as the key used in the future use of the returned token. A token
// is returned, that is usable for starting a SISU authorization flow,
// or to request a final XSTS token along with the user token.
func Authenticate(ctx context.Context, config xal.Config, proofKey *ecdsa.PrivateKey) (*Token, error) {
	c := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Renegotiation:      tls.RenegotiateOnceAsClient,
				InsecureSkipVerify: true,
			},
		},
	}
	defer c.CloseIdleConnections()

	id := uuid.NewString()
	switch config.Device.Type {
	case "Android":
		id = "{" + id + "}"
	case "Win32":
		id = strings.ToUpper(id)
	}

	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(request{
		RelyingParty: "http://auth.xboxlive.com",
		TokenType:    "JWT",
		Properties: properties{
			AuthMethod: "ProofOfPossession",
			ID:         id,
			DeviceType: config.Device.Type,
			Version:    config.Device.Version,
			ProofKey: jose.JSONWebKey{
				Key:       proofKey.Public(),
				Algorithm: string(jose.ES256),
				Use:       "sig",
			},
		},
	}); err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}
	defer buf.Reset()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://device.auth.xboxlive.com/device/authenticate", buf)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("x-xbl-contract-version", "1")
	req.Header.Set("User-Agent", config.UserAgent)
	req.Header.Set("Content-Type", "application/json")
	nsal.AuthPolicy.Sign(req, buf.Bytes(), proofKey)

	fmt.Println(string(buf.Bytes()))

	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("POST %s: %s", req.URL, resp.Status)
	}
	var t *Token
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode response body: %w", err)
	}
	return t, nil
}

func padTo32Bytes(i *big.Int) []byte {
	b := make([]byte, 32)
	i.FillBytes(b)
	return b
}

type (
	request    internal.TokenRequest[properties]
	properties struct {
		AuthMethod string
		ID         string `json:"Id"`
		DeviceType string
		Version    string
		ProofKey   jose.JSONWebKey
	}
)

const relyingParty = "http://auth.xboxlive.com"
