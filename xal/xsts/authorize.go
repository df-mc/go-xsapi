package xsts

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"

	"github.com/df-mc/go-xsapi/v2/xal"
	"github.com/df-mc/go-xsapi/v2/xal/internal"
	"github.com/df-mc/go-xsapi/v2/xal/xasd"
	"github.com/df-mc/go-xsapi/v2/xal/xast"
	"github.com/df-mc/go-xsapi/v2/xal/xasu"
)

type (
	// request represents the wire structure for the token request.
	request = internal.TokenRequest[properties]

	properties struct {
		SandboxID             string   `json:"SandboxId,omitempty"`
		DeviceToken           string   `json:"DeviceToken,omitempty"`
		TitleToken            string   `json:"TitleToken,omitempty"`
		UserTokens            []string `json:"UserTokens,omitempty"`
		OptionalDisplayClaims []string `json:"OptionalDisplayClaims,omitempty"`
	}
)

func Authorize(ctx context.Context, config xal.Config, proofKey *ecdsa.PrivateKey, relyingParty string, tokens []UnderlyingToken) (*Token, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("xal/xsts: no underlying tokens specified")
	}

	r := request{
		TokenType: "JWT",
		Properties: properties{
			SandboxID: config.Sandbox,
		},
		RelyingParty: relyingParty,
	}
	for _, token := range tokens {
		if token == nil {
			return nil, errors.New("xal/xsts: nil underlying token")
		}
		if !token.Valid() {
			return nil, fmt.Errorf("xal/xsts: invalid underlying %T", token)
		}
		switch token := token.(type) {
		case *xasd.Token:
			if r.Properties.DeviceToken != "" {
				return nil, errors.New("xal/xsts: duplicate device token")
			}
			r.Properties.DeviceToken = token.Token
		case *xast.Token:
			if r.Properties.TitleToken != "" {
				return nil, errors.New("xal/xsts: duplicate title token")
			}
			r.Properties.TitleToken = token.Token
		case *xasu.Token:
			r.Properties.UserTokens = append(r.Properties.UserTokens, token.Token)
		default:
			return nil, fmt.Errorf("xal/xsts: unsupported underlying token type %T", token)
		}
	}

	var token *Token
	if err := r.Do(ctx, config, "https://xsts.auth.xboxlive.com/xsts/authorize", proofKey, &token); err != nil {
		return nil, fmt.Errorf("xal/xsts: authorize: %w", err)
	}
	if !token.Valid() {
		return nil, errors.New("xal/xsts: invalid token response")
	}
	return token, nil
}

type UnderlyingToken interface {
	Valid() bool
}
