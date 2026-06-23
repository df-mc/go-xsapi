package internal

import (
	"time"
)

// expirationDelta is the safety margin before expiration when tokens stop being reusable.
const expirationDelta = time.Minute

// Token represents the basic structure of the Token issued by various
// Xbox Authentication Services (XAS). The C generic type indicates the
// struct type of the DisplayClaims field.
type Token[C any] struct {
	// IssueInstant is the time when the Token is created.
	IssueInstant time.Time
	// NotAfter is the expiration time of the Token.
	NotAfter time.Time
	// Token is the JWT for the Token.
	Token string
	// DisplayClaims contains additional data claimed from the issuer of
	// the Token. It generally contains fields about representing a user,
	// device, or title.
	DisplayClaims C
}

// Valid returns whether the Token is a valid token.
func (t *Token[C]) Valid() bool {
	return t != nil && ValidToken(t.Token, t.NotAfter)
}

// ValidToken reports whether token is non-empty and expires after the refresh
// margin used for Xbox authentication tokens.
func ValidToken(token string, notAfter time.Time) bool {
	return token != "" && time.Now().Before(notAfter.Add(-expirationDelta))
}
