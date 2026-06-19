package presence

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUpdateWithHeartbeatReturnsHeartbeatAfter(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", req.Method)
		}
		if got := req.URL.Path; got != "/users/xuid(1234)/devices/current/titles/current" {
			t.Fatalf("path = %q", got)
		}
		if got := req.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content type = %q, want application/json", got)
		}
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("{}")),
		}
		resp.Header.Set(HeaderHeartbeatAfter, "42")
		return resp, nil
	})}, xsts.UserInfo{XUID: "1234"})

	heartbeat, err := client.UpdateWithHeartbeat(context.Background(), TitleRequest{State: StateActive})
	if err != nil {
		t.Fatalf("UpdateWithHeartbeat returned error: %v", err)
	}
	if heartbeat != 42*time.Second {
		t.Fatalf("heartbeat = %s, want 42s", heartbeat)
	}
}

func TestParseHeartbeatAfterRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "soon"} {
		if got := ParseHeartbeatAfter(value); got != 0 {
			t.Fatalf("ParseHeartbeatAfter(%q) = %s, want 0", value, got)
		}
	}
}
