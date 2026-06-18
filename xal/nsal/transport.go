package nsal

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/df-mc/go-xsapi/v2/xal/internal/timestamp"
)

var _ http.RoundTripper = (*Transport)(nil)

// TransportTokenSource supplies authorization tokens and the proof key used to
// sign requests.
type TransportTokenSource interface {
	Token(ctx context.Context, relyingParty string) (Token, error)
	ProofKey() *ecdsa.PrivateKey
}

// Transport is an [http.RoundTripper] that resolves outgoing request URLs using
// NSAL, then applies the required XSTS token and request signature.
type Transport struct {
	// Base is the underlying transport used to perform HTTP requests after
	// authentication headers are applied. If nil, [http.DefaultTransport] is used.
	Base http.RoundTripper

	// Resolver resolves endpoint and signature policy data for request URLs.
	Resolver *Resolver

	// TokenSource supplies authorization tokens and the proof key used by signatures.
	TokenSource TransportTokenSource
}

// RoundTrip implements [http.RoundTripper].
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBodyClosed bool
	if req.Body != nil {
		defer func() {
			if !reqBodyClosed {
				_ = req.Body.Close()
			}
		}()
	}

	if req.Header.Get("Authorization") != "" {
		reqBodyClosed = true
		return t.baseTransport().RoundTrip(req)
	}

	ctx := req.Context()
	exclusion, _ := ctx.Value(headerExclusion{}).(headerExclusionSet)
	if exclusion.authorization() {
		reqBodyClosed = true
		return t.baseTransport().RoundTrip(req)
	}

	token, policy, err := t.TokenAndSignature(ctx, req.URL)
	if err != nil {
		return nil, fmt.Errorf("request XSTS token and signature: %w", err)
	}

	req2 := req.Clone(ctx)
	token.SetAuthHeader(req2)

	if req2.Header.Get("Signature") == "" && !exclusion.signature() {
		var data []byte
		if req.Body != nil {
			signingBuffer := &bytes.Buffer{}
			if _, err := signingBuffer.ReadFrom(req.Body); err != nil {
				signingBuffer.Reset()
				return nil, fmt.Errorf("clone request body: %w", err)
			}
			data, req2.Body = signingBuffer.Bytes(), io.NopCloser(signingBuffer)
		}
		if err := policy.Sign(req2, data, t.TokenSource.ProofKey(), timestamp.Now()); err != nil {
			return nil, fmt.Errorf("sign request: %w", err)
		}
	}

	return t.baseTransport().RoundTrip(req2)
}

// TokenAndSignature resolves an XSTS token and signature policy for the given URL.
func (t *Transport) TokenAndSignature(ctx context.Context, u *url.URL) (_ Token, policy SignaturePolicy, _ error) {
	if t == nil {
		return nil, policy, errors.New("xal/nsal: nil Transport")
	}
	if t.TokenSource == nil {
		return nil, policy, errors.New("xal/nsal: nil TokenSource")
	}
	endpoint, policy, ok := t.Resolver.Match(u)
	if !ok {
		return nil, policy, fmt.Errorf("no endpoint was found for %s", u)
	}

	token, err := t.TokenSource.Token(ctx, endpoint.RelyingParty)
	if err != nil {
		return nil, policy, fmt.Errorf("request XSTS token: %w", err)
	}
	return token, policy, nil
}

func (t *Transport) baseTransport() http.RoundTripper {
	if t != nil && t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

// WithoutAuthHeaders returns a cloned HTTP request configured to exclude
// specified authentication headers from being automatically added by [Transport].
//
// Header names are matched case-insensitively. If no headers are provided,
// both Authorization and Signature are excluded.
func WithoutAuthHeaders(req *http.Request, headers ...string) *http.Request {
	if len(headers) == 0 {
		headers = []string{"Authorization", "Signature"}
	}
	return req.Clone(context.WithValue(req.Context(), headerExclusion{}, headerExclusionSet(headers)))
}

// headerExclusion is a context key that stores which authentication headers
// should be excluded from automatic generation by [Transport].
type headerExclusion struct{}

// headerExclusionSet represents a list of header names to exclude from
// automatic authentication header generation. Header names are case-insensitive.
type headerExclusionSet []string

func (s headerExclusionSet) contains(header string) bool {
	return slices.ContainsFunc(s, func(s string) bool {
		return strings.EqualFold(s, header)
	})
}

func (s headerExclusionSet) authorization() bool {
	return s.contains("Authorization")
}

func (s headerExclusionSet) signature() bool {
	return s.contains("Signature")
}
