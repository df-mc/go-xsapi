package xast

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"

	"github.com/df-mc/go-xsapi/xal"
	"github.com/df-mc/go-xsapi/xal/internal"
	"github.com/df-mc/go-xsapi/xal/xasd"
	"github.com/go-jose/go-jose/v4"
)

func Authenticate(ctx context.Context, config xal.Config, deviceToken *xasd.Token, proofKey *ecdsa.PrivateKey) (*Token, error) {
	var (
		r = request{
			RelyingParty: "http://auth.xboxlive.com",
			TokenType:    "JWT",
			Properties: properties{
				DeviceToken: deviceToken.Token,
				ProofKey: jose.JSONWebKey{
					Key: proofKey.Public(),
					Use: "sig",
				},
				TitleID: config.TitleID,
			},
		}
		t *Token
	)
	if err := r.Do(ctx, config, "https://title.auth.xboxlive.com/title/authenticate", proofKey, &t); err != nil {
		return nil, fmt.Errorf("xal/xast: authenticate: %w", err)
	}
	if !t.Valid() {
		return nil, errors.New("xal/xast: invalid token response")
	}
	return t, nil
}

type (
	request    = internal.TokenRequest[properties]
	properties struct {
		DeviceToken string
		ProofKey    jose.JSONWebKey
		TitleID     int64 `json:"TitleId"`
	}
)
