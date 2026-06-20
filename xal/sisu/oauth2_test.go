package sisu

import (
	"context"
	"errors"
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
	if got := oauth2ContextClient(ctx); got != http.DefaultClient {
		t.Fatalf("client = %p, want default client %p", got, http.DefaultClient)
	}
}

func TestTokenSourceRefreshErrorIncludesStatusError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	src := (Config{ClientID: "client"}).TokenSource(ctx, &oauth2.Token{
		AccessToken:  "expired",
		RefreshToken: "refresh",
		TokenType:    "bearer",
		Expiry:       time.Now().Add(-time.Minute),
	})

	_, err := src.Token()
	if err == nil {
		t.Fatal("Token() error = nil")
	}
	var statusErr *xal.StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("Token() error = %T %[1]v, want xal.StatusError", err)
	}
	if statusErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("StatusCode = %v, want %v", statusErr.StatusCode, http.StatusUnauthorized)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
