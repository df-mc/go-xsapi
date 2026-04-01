package xsapi

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/df-mc/go-xsapi/mpsd"
	"github.com/df-mc/go-xsapi/presence"
	"github.com/df-mc/go-xsapi/social"
	"github.com/df-mc/go-xsapi/xal/xsts"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestClientCloseContextRetriesAfterSubclientFailure(t *testing.T) {
	userInfo := xsts.UserInfo{XUID: "2533274799999999"}
	presenceErr := errors.New("presence remove failed")
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if req.Method != http.MethodDelete {
			t.Fatalf("request method = %s, want DELETE", req.Method)
		}
		return nil, presenceErr
	})}

	client := &Client{
		mpsd:     mpsd.New(httpClient, nil, userInfo, nil),
		social:   social.New(httpClient, nil, userInfo, nil),
		presence: presence.New(httpClient, userInfo),
	}

	if err := client.CloseContext(context.Background()); !errors.Is(err, presenceErr) {
		t.Fatalf("first close error = %v, want %v", err, presenceErr)
	}
	if client.closed {
		t.Fatal("client was marked closed after failed cleanup")
	}

	if err := client.CloseContext(context.Background()); !errors.Is(err, presenceErr) {
		t.Fatalf("second close error = %v, want %v", err, presenceErr)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("presence close attempts = %d, want 2", got)
	}
}
