package xasu

import (
	"context"

	"github.com/yomoggies/xsapi-go/xal/internal"
)

type Token = internal.Token[DisplayClaims]

type DisplayClaims struct {
	// UserInfo contains an information about the authenticated user.
	UserInfo []UserInfo `json:"xui"`
}

// UserInfo encapsulates an information about a user in Xbox Live.
// It is claimed by user Token.
type UserInfo struct {
	// UserHash is the unique hash of the user, used in the 'Authorization' token.
	UserHash string `json:"uhs"`
}

type TokenSource interface {
	UserToken(ctx context.Context) (*Token, error)
}
