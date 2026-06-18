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
	// Titles lists NSAL title data sources in precedence order. Earlier
	// entries are matched before later entries.
	Titles []*TitleData
}

// NewResolver returns a Resolver using title-specific NSAL data for the
// authenticated title before the default NSAL title data.
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
		Titles: []*TitleData{currentTitle, defaultTitle},
	}, nil
}

// Match resolves the endpoint and signature policy that apply to u.
func (r *Resolver) Match(u *url.URL) (endpoint Endpoint, policy SignaturePolicy, ok bool) {
	if r == nil {
		return endpoint, policy, false
	}
	for _, title := range r.Titles {
		if title == nil {
			continue
		}
		if endpoint, policy, ok = title.Match(u); ok {
			return endpoint, policy, true
		}
	}
	return endpoint, policy, false
}
