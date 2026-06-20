package sisu

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/xal"
	"golang.org/x/oauth2"
)

func TestOAuth2ContextClientIgnoresTypedNil(t *testing.T) {
	var client *http.Client
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	if got := oauth2ContextClient(ctx); got == nil {
		t.Fatal("client is nil")
	} else if got == http.DefaultClient {
		t.Fatal("client = http.DefaultClient, want cloned client")
	} else if got.Timeout != oauth2RequestTimeout {
		t.Fatalf("client timeout = %v, want %v", got.Timeout, oauth2RequestTimeout)
	}
}

func TestOAuth2ContextClientUsesXALClient(t *testing.T) {
	base := &http.Client{}
	ctx := context.WithValue(context.Background(), xal.HTTPClient, base)
	got := oauth2ContextClient(ctx)
	if got == base {
		t.Fatal("client was not cloned")
	}
	if got.Timeout != oauth2RequestTimeout {
		t.Fatalf("client timeout = %v, want %v", got.Timeout, oauth2RequestTimeout)
	}
}

func TestOAuth2ContextClientPreservesConfiguredTimeout(t *testing.T) {
	base := &http.Client{Timeout: time.Minute}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, base)
	if got := oauth2ContextClient(ctx); got != base {
		t.Fatalf("client = %p, want original client %p", got, base)
	}
}

func TestTokenSourceRefreshErrorIncludesOAuthBody(t *testing.T) {
	base := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"refresh expired"}`), nil
	})}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, base)
	src := (Config{ClientID: "client"}).TokenSource(ctx, &oauth2.Token{
		AccessToken:  "expired",
		RefreshToken: "refresh",
		TokenType:    "bearer",
		Expiry:       time.Now().Add(-time.Hour),
	})

	_, err := src.Token()
	if err == nil {
		t.Fatal("Token succeeded, want error")
	}
	if got := err.Error(); !strings.Contains(got, "invalid_grant: refresh expired") {
		t.Fatalf("error = %q, want OAuth body detail", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
