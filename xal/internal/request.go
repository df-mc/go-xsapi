package internal

type TokenRequest[P any] struct {
	// RelyingParty is a URI representing the relying-party, which the token should be
	// authorized for. It is typically 'http' or 'rp' URI.
	RelyingParty string

	// TokenType indicates that type of the token.
	// It is typically 'JWT' for most tokens.
	TokenType string

	// Properties is the properties specific to the token.
	// It may contain the 'ProofKey' field, which is a JWK object
	// representing the key used for the device token.
	Properties P
}
