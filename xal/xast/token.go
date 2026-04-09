package xast

import (
	"context"

	"github.com/df-mc/go-xsapi/v2/xal/internal"
)

// Token is an XAST (Xbox Authentication Services for Titles) token that
// authenticates a title on Xbox Live. A title corresponds to a specific game
// or application registered with Xbox Live.
type Token = internal.Token[DisplayClaims]

// DisplayClaims holds the metadata embedded in a title token.
type DisplayClaims struct {
	// TitleInfo contains the information about the authenticated title
	// as claimed by the token.
	TitleInfo TitleInfo `json:"xti"`
}

// TitleInfo holds the identity information for a title as claimed by a [Token].
type TitleInfo struct {
	// TitleID is the Xbox Live title ID, represented as a decimal integer
	// encoded in string form.
	TitleID string `json:"tid"`
}

// TokenSource is the interface that supplies title tokens for authenticating
// a title on Xbox Live.
type TokenSource interface {
	TitleToken(ctx context.Context) (*Token, error)
}
