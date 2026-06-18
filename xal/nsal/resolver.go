package nsal

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"net/url"
)

// Resolver resolves the Xbox Live endpoint and signature policy for outgoing
// request URLs.
type Resolver struct {
	// Current is the title-specific NSAL data for the authenticated title.
	// It takes precedence over Default when both match the same URL.
	Current *TitleData

	// Default is the fallback NSAL data for generic Xbox Live endpoints.
	Default *TitleData
}

// NewResolver returns a Resolver using the default NSAL title data and the
// title-specific NSAL data for the authenticated title.
func NewResolver(ctx context.Context, token Token, proofKey *ecdsa.PrivateKey) (*Resolver, error) {
	defaultTitle, err := Default(ctx)
	if err != nil {
		return nil, fmt.Errorf("request default title data: %w", err)
	}
	currentTitle, err := Current(ctx, token, proofKey)
	if err != nil {
		return nil, fmt.Errorf("request current title data: %w", err)
	}
	return &Resolver{
		Current: currentTitle,
		Default: defaultTitle,
	}, nil
}

// Match resolves the endpoint and signature policy that apply to u.
func (r *Resolver) Match(u *url.URL) (endpoint Endpoint, policy SignaturePolicy, ok bool) {
	if r == nil {
		return endpoint, policy, false
	}
	if r.Current != nil {
		if endpoint, policy, ok = r.Current.Match(u); ok {
			return endpoint, policy, true
		}
	}
	if r.Default != nil {
		return r.Default.Match(u)
	}
	return endpoint, policy, false
}
