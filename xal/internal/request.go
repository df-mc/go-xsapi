package internal

import (
	"crypto/ecdsa"

	"github.com/go-jose/go-jose/v4"
)

// TokenRequest represents the wire structure of the request used for requesting
// tokens in Xbox Authentication Services. Make sure to specify the P generic type
// to whatever you want to specify in the Properties field.
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

func ProofKey(key *ecdsa.PrivateKey) jose.JSONWebKey {
	return jose.JSONWebKey{
		Key:       key.Public(),
		Algorithm: string(jose.ES256),
		Use:       "sig",
	}
}
