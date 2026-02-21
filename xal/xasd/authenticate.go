package xasd

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"strings"

	"github.com/df-mc/go-xsapi/xal"
	"github.com/df-mc/go-xsapi/xal/internal"
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

	var (
		r = request{
			RelyingParty: "http://auth.xboxlive.com",
			TokenType:    "JWT",
			Properties: properties{
				AuthMethod: "ProofOfPossession",
				ID:         id,
				DeviceType: config.Device.Type,
				Version:    config.Device.Version,
				ProofKey:   internal.ProofKey(proofKey),
			},
		}
		t *Token
	)
	if err := r.Do(ctx, "https://device.auth.xboxlive.com/device/authenticate", config.UserAgent, proofKey, &t); err != nil {
		return nil, fmt.Errorf("xal/xasd: authenticate: %w", err)
	}
	if !t.Valid() {
		return nil, errors.New("xal/xasd: invalid token response")
	}
	return t, nil
}

type (
	// request represents the wire structure used for requesting a device token.
	request = internal.TokenRequest[properties]

	// properties represents the properties used to request a device token.
	properties struct {
		// AuthMethod is either 'ProofOfPossession' or 'RPS'.
		// When 'RPS', the RPSTicket must be present, which
		// contains the access token for user in Windows.
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
