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
)

func TestTransportRoundTripSignsRequest(t *testing.T) {
	key := mustGenerateKey(t)
	src := &transportTokenSource{
		token:    authorizationToken("XBL3.0 x=uhs;token"),
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
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
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
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if src.called {
		t.Fatal("token source was called despite existing Authorization header")
	}
}

func TestTransportRoundTripWithoutSignature(t *testing.T) {
	key := mustGenerateKey(t)
	src := &transportTokenSource{
		token:    authorizationToken("XBL3.0 x=uhs;token"),
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
	if _, err := transport.RoundTrip(WithoutAuthHeaders(req, "Signature")); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
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
	if _, err := transport.RoundTrip(WithoutAuthHeaders(req)); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
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

type authorizationToken string

func (t authorizationToken) SetAuthHeader(req *http.Request) {
	req.Header.Set("Authorization", string(t))
}

type transportTokenSource struct {
	called       bool
	relyingParty string
	token        Token
	proofKey     *ecdsa.PrivateKey
}

func (src *transportTokenSource) Token(_ context.Context, relyingParty string) (Token, error) {
	src.called = true
	src.relyingParty = relyingParty
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
