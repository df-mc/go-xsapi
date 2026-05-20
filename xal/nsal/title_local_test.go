package nsal

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/df-mc/go-xsapi/v2/xal"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type testToken struct{}

func (testToken) SetAuthHeader(req *http.Request) {}

func TestTitleReturnsSignErrorBeforeSendingRequest(t *testing.T) {
	usedTransport := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		usedTransport = true
		return nil, errors.New("unexpected request")
	})}
	ctx := context.WithValue(context.Background(), xal.HTTPClient, client)

	_, err := Title(ctx, testToken{}, nil, "current")
	if err == nil || !strings.Contains(err.Error(), "sign request") {
		t.Fatalf("Title error = %v, want sign request failure", err)
	}
	if usedTransport {
		t.Fatal("Title sent request despite local signing failure")
	}
}
