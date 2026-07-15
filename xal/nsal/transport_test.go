package nsal

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/df-mc/go-xsapi/v2/xal/xasu"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

func TestTransportRoundTripSignsRequest(t *testing.T) {
	key := mustGenerateKey(t)
	src := &transportTokenSource{
		token:    authorizationToken("token"),
		proofKey: key,
	}
	body := &trackingBody{ReadCloser: io.NopCloser(strings.NewReader("payload"))}
	var baseCalled bool

	transport := &Transport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			baseCalled = true
			if src.relyingParty != "https://multiplayer.minecraft.net/" {
				t.Fatalf("relying party = %q, want https://multiplayer.minecraft.net/", src.relyingParty)
			}
			if got := req.Header.Get("Authorization"); got != "XBL3.0 x=uhs;token" {
				t.Fatalf("Authorization = %q, want token header", got)
			}
			if got := req.Header.Get("Signature"); got == "" {
				t.Fatal("Signature header was not set")
			}
			data, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read cloned body: %v", err)
			}
			if string(data) != "payload" {
				t.Fatalf("body = %q, want payload", data)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
		Resolver: testResolver(src),
	}

	req, err := http.NewRequest(http.MethodPost, "https://multiplayer.minecraft.net/authentication", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if !baseCalled {
		t.Fatal("base transport was not called")
	}
	if !body.closed {
		t.Fatal("original request body was not closed")
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("original Authorization header was mutated to %q", got)
	}
	if got := req.Header.Get("Signature"); got != "" {
		t.Fatalf("original Signature header was mutated to %q", got)
	}
}

func TestTransportRoundTripWithNilResolverReturnsError(t *testing.T) {
	transport := &Transport{}
	req, err := http.NewRequest(http.MethodGet, "https://multiplayer.minecraft.net/authentication", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	_, err = transport.RoundTrip(req)
	if err == nil || !strings.Contains(err.Error(), "xal/nsal: nil Resolver") {
		t.Fatalf("RoundTrip error = %v, want nil Resolver error", err)
	}
}

func TestTransportRoundTripRefreshesExpiredXSTSToken(t *testing.T) {
	key := mustGenerateKey(t)
	stale := authorizationToken("stale")
	fresh := authorizationToken("fresh")
	src := &refreshingTransportTokenSource{
		transportTokenSource: transportTokenSource{token: stale, proofKey: key},
		fresh:                fresh,
	}
	firstResponseBody := &trackingBody{ReadCloser: http.NoBody}
	var requests int
	transport := &Transport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			body, err := io.ReadAll(req.Body)
			if err != nil || string(body) != "payload" {
				t.Fatalf("request %d body = %q, err = %v", requests, body, err)
			}
			if requests == 1 {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Header:     http.Header{"Www-Authenticate": {"Token error='token_expired'"}},
					Body:       firstResponseBody,
				}, nil
			}
			if requests != 2 {
				t.Fatalf("unexpected request %d", requests)
			}
			if req.GetBody == nil {
				t.Fatal("retried request is not replayable")
			}
			if got := req.Header.Get("Authorization"); got != "XBL3.0 x=uhs;fresh" {
				t.Fatalf("Authorization = %q, want fresh token", got)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
		Resolver: testResolver(src),
	}

	req, err := http.NewRequest(http.MethodPut, "https://multiplayer.minecraft.net/authentication", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.GetBody = nil
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if src.invalidated != stale {
		t.Fatal("invalidated token was not the rejected token")
	}
	if !firstResponseBody.closed {
		t.Fatal("first response body was not closed before retry")
	}
}

func TestTokenExpired(t *testing.T) {
	for name, tc := range map[string]struct {
		headers []string
		want    bool
	}{
		"first parameter": {[]string{"Token error='token_expired'"}, true},
		"no comma space":  {[]string{"Token realm='xboxlive.com',error='token_expired'"}, true},
		"later header":    {[]string{"Token error='token_required'", "Token error='token_expired'"}, true},
		"other error":     {[]string{"Token error='token_required'"}, false},
		"provider error":  {[]string{"Token provider_error='token_expired'"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{"Www-Authenticate": tc.headers}}
			if got := tokenExpired(resp); got != tc.want {
				t.Fatalf("tokenExpired = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestTransportRoundTripUsesExistingAuthorization(t *testing.T) {
	src := &transportTokenSource{token: authorizationToken("unexpected")}
	transport := &Transport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer existing" {
				t.Fatalf("Authorization = %q, want existing header", got)
			}
			if got := req.Header.Get("Signature"); got != "" {
				t.Fatalf("Signature = %q, want empty", got)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
		Resolver: testResolver(src),
	}

	req, err := http.NewRequest(http.MethodGet, "https://multiplayer.minecraft.net/authentication", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer existing")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if src.called {
		t.Fatal("token source was called despite existing Authorization header")
	}
}

func TestTransportRoundTripWithoutSignature(t *testing.T) {
	key := mustGenerateKey(t)
	src := &transportTokenSource{
		token:    authorizationToken("token"),
		proofKey: key,
	}
	transport := &Transport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "XBL3.0 x=uhs;token" {
				t.Fatalf("Authorization = %q, want token header", got)
			}
			if got := req.Header.Get("Signature"); got != "" {
				t.Fatalf("Signature = %q, want empty", got)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
		Resolver: testResolver(src),
	}

	req, err := http.NewRequest(http.MethodGet, "https://multiplayer.minecraft.net/authentication", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := transport.RoundTrip(WithoutAuthHeaders(req, "Signature"))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
}

func TestTransportRoundTripWithoutAuthHeaders(t *testing.T) {
	src := &transportTokenSource{token: authorizationToken("unexpected")}
	transport := &Transport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "" {
				t.Fatalf("Authorization = %q, want empty", got)
			}
			if got := req.Header.Get("Signature"); got != "" {
				t.Fatalf("Signature = %q, want empty", got)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
		Resolver: testResolver(src),
	}

	req, err := http.NewRequest(http.MethodGet, "https://multiplayer.minecraft.net/authentication", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := transport.RoundTrip(WithoutAuthHeaders(req))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()
	if src.called {
		t.Fatal("token source was called despite WithoutAuthHeaders")
	}
}

func TestTransportTokenAndSignatureRejectsUnknownEndpoint(t *testing.T) {
	transport := &Transport{
		Resolver: testResolver(&transportTokenSource{token: authorizationToken("token")}),
	}
	u := mustParseURL(t, "https://example.com")
	if _, _, err := transport.TokenAndSignature(context.Background(), u); err == nil {
		t.Fatal("TokenAndSignature returned nil error for unknown endpoint")
	}
}

func authorizationToken(s string) *xsts.Token {
	return &xsts.Token{
		DisplayClaims: xsts.DisplayClaims{
			UserInfo: []xsts.UserInfo{
				{
					UserInfo: xasu.UserInfo{
						UserHash: "uhs",
					},
				},
			},
		},
		Token: s,
	}
}

type transportTokenSource struct {
	called       bool
	calls        int
	relyingParty string
	token        *xsts.Token
	proofKey     *ecdsa.PrivateKey
	err          error
}

type refreshingTransportTokenSource struct {
	transportTokenSource
	fresh       *xsts.Token
	invalidated *xsts.Token
}

func (src *refreshingTransportTokenSource) InvalidateXSTSToken(token *xsts.Token) {
	src.invalidated = token
	if src.token == token {
		src.token = src.fresh
	}
}

func (src *transportTokenSource) XSTSToken(_ context.Context, relyingParty string) (*xsts.Token, error) {
	src.called = true
	src.calls++
	src.relyingParty = relyingParty
	if src.err != nil {
		return nil, src.err
	}
	return src.token, nil
}

func (src *transportTokenSource) ProofKey() *ecdsa.PrivateKey {
	return src.proofKey
}

func testResolver(src TokenSource) *Resolver {
	return ResolverConfig{TitleIDs: []string{}, Titles: []*TitleData{{
		Endpoints: []Endpoint{{
			Protocol:     "https",
			Host:         "multiplayer.minecraft.net",
			HostType:     HostTypeFQDN,
			RelyingParty: "https://multiplayer.minecraft.net/",
		}},
	}}}.New(src)
}

func mustGenerateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

type trackingBody struct {
	io.ReadCloser
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return b.ReadCloser.Close()
}
