package notification

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip handles a test request with the supplied function.
func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUpdateSendsJSONContentType(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Request:    req,
		}, nil
	})}, xsts.UserInfo{XUID: "1234"}, nil)

	err := client.Update(context.Background(), []Notification{&Unknown{
		Category: SubscriptionCategoryPeople,
		Type:     SubscriptionTypeFollowers,
		ID:       "notification-1",
	}}, UpdateTypeLastSeen, time.Now())
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
}
