package xasu

import (
	"context"

	"github.com/df-mc/go-xsapi/xal/internal"
)

// Token is an XASU (Xbox Authentication Services for Users) token that
// authenticates a single user account on Xbox Live. A device may have
// multiple user accounts signed in simultaneously (for example, multiple
// Microsoft Accounts on a shared Windows or Android device), and a separate
// user token is issued for each account to identify it individually.
//
// User tokens are normally obtained by exchanging a Microsoft Account OAuth2
// access token.
type Token = internal.Token[DisplayClaims]

// DisplayClaims holds the metadata embedded in a user token.
type DisplayClaims struct {
	// UserInfo contains information about the authenticated user as claimed
	// by the token.
	UserInfo []UserInfo `json:"xui"`
}

// UserInfo holds the identity information for a single Xbox Live user as
// claimed by a [Token].
type UserInfo struct {
	// UserHash is a hash that identifies the user within an 'Authorization'
	// header. It is unique per token issuance and should not be treated as a
	// stable identifier across sessions.
	UserHash string `json:"uhs"`
}

// TokenSource is the interface that supplies user tokens for authenticating
// individual Xbox Live user accounts.
type TokenSource interface {
	UserToken(ctx context.Context) (*Token, error)
}
