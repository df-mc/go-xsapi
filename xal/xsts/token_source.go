package xsts

import "context"

// TokenSource provides XSTS (Xbox Secure Token Service) tokens for an
// authenticated user.
//
// A TokenSource encapsulates the logic required to obtain an XSTS token
// using previously established device (XASD), title (XAST), and user (XASU)
// authentication tokens. Implementations are responsible for coordinating
// any underlying authentication flows and for applying the correct security
// policies required by XSTS.
//
// Typical implementations include SISU-based authenticators and other
// platform-specific XSTS clients.
type TokenSource interface {
	// XSTSToken returns an XSTS token issued for the specified relying party.
	//
	// The relying party identifies the service for which the token is valid
	// and determines which claims are included in the resulting token. Common
	// relying parties include:
	//   - "http://xboxlive.com" for Xbox Live user services
	//   - Title- or service-specific endpoints
	//
	// In most cases, the relying party is resolved automatically using
	// NSAL (Network Security Allow List) based on the request URL by the caller.
	//
	// The provided context controls the lifetime of the request and may be
	// used to cancel in-flight authentication operations.
	//
	// Implementations may cache and reuse tokens until they expire
	XSTSToken(ctx context.Context, relyingParty string) (*Token, error)
}
