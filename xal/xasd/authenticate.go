package xasd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
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
	id := uuid.NewString()
	switch config.Device.Type {
	case xal.DeviceTypeAndroid:
		id = "{" + id + "}"
	case xal.DeviceTypeWin32:
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
			ProofKey:   internal.ProofKey(proofKey),
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

	resp, err := internal.ContextClient(ctx).Do(req)
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

type (
	// request represents the wire structure used for requesting a device token.
	request internal.TokenRequest[properties]

	// properties represents the properties used to request a device token.
	properties struct {
		// AuthMethod is either 'ProofOfPossession' or 'RPS'.
		// When 'RPS', the access token for the user in Windows
		// should be present in the RPSTicket.
		AuthMethod string
		// ID is the unique ID used to associate a device.
		ID string `json:"Id"`
		// DeviceType is the [xal.Device.Type].
		DeviceType string
		// Version is the [xal.Device.Version].
		Version string
		// ProofKey is the proof key used to sign requests.
		ProofKey jose.JSONWebKey
		// RPSTicket is the access token for the Microsoft Account
		// of the user. It is used in Windows devices.
		// RPSTicket should either contain a prefix with 'd=' (Delegated Token?)
		// or 't=' (Compact Token?).
		RPSTicket string `json:",omitempty"`
	}
)
