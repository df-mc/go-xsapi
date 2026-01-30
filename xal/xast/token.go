package xast

import (
	"context"

	"github.com/yomoggies/xsapi-go/xal/internal"
)

type Token = internal.Token[DisplayClaims]

type DisplayClaims struct {
	// TitleInfo contains the information about the authenticated title
	// claimed by the Token.
	TitleInfo TitleInfo `json:"xti"`
}

type TitleInfo struct {
	// TitleID is the title ID specific to Xbox Live.
	// It is a numerical ID present as a string.
	TitleID string `json:"tid"`
}

type TokenSource interface {
	TitleToken(ctx context.Context) (*Token, error)
}
