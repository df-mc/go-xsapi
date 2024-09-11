package xsapi

import (
	"net/http"
)

type Token interface {
	// SetAuthHeader sets an 'Authorization' and a 'Signature' header in the request.
	SetAuthHeader(req *http.Request)
	// String formats the Token into a string that can be set as an 'Authorization' header
	// or a field in requests. It usually follows the format 'XBL3.0 x=<user hash>;<token>'.
	String() string
	// DisplayClaims returns the DisplayClaims, which contains an information for a user.
	// It is usually claimed from the response body returned from the authorization.
	DisplayClaims() DisplayClaims
}

// TokenSource implements a Token method that returns a Token.
type TokenSource interface {
	Token() (Token, error)
}

// DisplayClaims contains an information for user of Token.
type DisplayClaims struct {
	GamerTag string `json:"gtg"`
	XUID     string `json:"xid"`
	UserHash string `json:"uhs"`
}
