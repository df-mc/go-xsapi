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

func TestOAuth2ClientUsesXALClient(t *testing.T) {
	base := &http.Client{}
	ctx := context.WithValue(context.Background(), xal.HTTPClient, base)
	got := oauth2Client(ctx)
	if got == base {
		t.Fatal("client was not cloned")
	}
	if got.Timeout != oauth2RequestTimeout {
		t.Fatalf("client timeout = %v, want %v", got.Timeout, oauth2RequestTimeout)
	}
}

func TestOAuth2ClientPreservesConfiguredTimeout(t *testing.T) {
	base := &http.Client{Timeout: time.Minute}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, base)
	if got := oauth2Client(ctx); got != base {
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
	var retrieveError *oauth2.RetrieveError
	if !errors.As(err, &retrieveError) {
		t.Fatalf("error = %q, want oauth2.RetrieveError", err)
	}
}

func TestExchangeUsesXALContextClient(t *testing.T) {
	defaultTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("default transport used")
	})
	defer func() {
		http.DefaultTransport = defaultTransport
	}()

	var calls int
	base := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(http.StatusOK, `{"access_token":"access","token_type":"bearer","refresh_token":"refresh","expires_in":3600}`), nil
	})}
	ctx := context.WithValue(context.Background(), xal.HTTPClient, base)

	token, err := (Config{ClientID: "client"}).Exchange(ctx, "code")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if token.AccessToken != "access" {
		t.Fatalf("access token = %q, want access", token.AccessToken)
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
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
