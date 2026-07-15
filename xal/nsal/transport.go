package nsal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/df-mc/go-xsapi/v2/xal/internal/timestamp"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

var _ http.RoundTripper = (*Transport)(nil)

// Transport is an [http.RoundTripper] that resolves outgoing request URLs using
// NSAL, then applies the required XSTS token and request signature.
//
// Transport only sends the final authenticated request through [Transport.Base].
// If [Resolver] must load NSAL title data first, that lookup uses the HTTP
// client from the request context as documented on [Resolver].
type Transport struct {
	// Base is the underlying transport used to perform HTTP requests after
	// authentication headers are applied. If nil, [http.DefaultTransport] is used.
	Base http.RoundTripper

	// Resolver resolves endpoint and signature policy data for request URLs.
	Resolver *Resolver
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

	var data []byte
	if req.Body != nil {
		var err error
		data, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
	}

	return t.roundTripAuthenticated(req, exclusion, data)
}

// roundTripAuthenticated signs and sends req, retrying once when Xbox reports
// that the XSTS token expired before its advertised lifetime.
func (t *Transport) roundTripAuthenticated(req *http.Request, exclusion headerExclusionSet, data []byte) (*http.Response, error) {
	ctx := req.Context()
	for attempt := 0; ; attempt++ {
		token, policy, err := t.TokenAndSignature(ctx, req.URL)
		if err != nil {
			return nil, fmt.Errorf("request XSTS token and signature: %w", err)
		}

		req2 := req.Clone(ctx)
		if req.Body != nil {
			req2.Body = io.NopCloser(bytes.NewReader(data))
			req2.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(data)), nil
			}
		}
		token.SetAuthHeader(req2)

		if req2.Header.Get("Signature") == "" && !exclusion.signature() {
			if err := policy.Sign(req2, data, t.Resolver.src.ProofKey(), timestamp.Now()); err != nil {
				return nil, fmt.Errorf("sign request: %w", err)
			}
		}

		resp, err := t.baseTransport().RoundTrip(req2)
		if err != nil {
			return nil, err
		}
		invalidator, ok := t.Resolver.src.(xstsTokenInvalidator)
		if attempt > 0 || !ok || !tokenExpired(resp) {
			return resp, nil
		}
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		invalidator.InvalidateXSTSToken(token)
	}
}

// tokenExpired reports whether Xbox explicitly rejected an expired token.
func tokenExpired(resp *http.Response) bool {
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return false
	}
	for _, value := range resp.Header.Values("WWW-Authenticate") {
		for part := range strings.SplitSeq(value, ",") {
			part = strings.TrimSpace(strings.ToLower(part))
			part = strings.TrimSpace(strings.TrimPrefix(part, "token "))
			if part == "error='token_expired'" {
				return true
			}
		}
	}
	return false
}

// xstsTokenInvalidator lets token sources discard a token rejected upstream.
type xstsTokenInvalidator interface {
	InvalidateXSTSToken(*xsts.Token)
}

// TokenAndSignature resolves an XSTS token and signature policy for the given URL.
func (t *Transport) TokenAndSignature(ctx context.Context, u *url.URL) (_ *xsts.Token, policy SignaturePolicy, _ error) {
	if t == nil {
		return nil, policy, errors.New("xal/nsal: nil Transport")
	}
	if t.Resolver == nil {
		return nil, policy, errors.New("xal/nsal: nil Resolver")
	}
	return t.Resolver.TokenAndSignature(ctx, u)
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
