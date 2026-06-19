package presence

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUpdateReturnsResult(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		heartbeat time.Duration
	}{
		{name: "returns heartbeat header", header: "17", heartbeat: 17 * time.Second},
		{name: "missing heartbeat header returns zero"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := New(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				header := make(http.Header)
				if tt.header != "" {
					header.Set("X-Heartbeat-After", tt.header)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     header,
					Body:       http.NoBody,
					Request:    req,
				}, nil
			})}, xsts.UserInfo{XUID: "1234"})

			result, err := client.Update(context.Background(), TitleRequest{
				State: StateActive,
			})
			if err != nil {
				t.Fatalf("Update returned error: %v", err)
			}
			if result.HeartbeatAfter != tt.heartbeat {
				t.Fatalf("heartbeat = %v, want %v", result.HeartbeatAfter, tt.heartbeat)
			}
		})
	}
}
