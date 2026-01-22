package xsts

import "context"

type TokenSource interface {
	// XSTSToken requests an XSTS token that relies on a specific party.
	// The relying-party is usually resolved using an NSAL endpoint.
	XSTSToken(ctx context.Context, relyingParty string) (*Token, error)
}

type tokenSource struct {
}
