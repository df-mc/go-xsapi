package social

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFollowersQueriesPeopleHubFollowers(t *testing.T) {
	var requested bool
	client := New(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requested = true
		if req.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", req.Method)
		}
		if got := req.URL.Path; got != "/users/me/people/followers/decoration/bio,detail,multiplayerSummary,preferredColor,presenceDetail" {
			t.Fatalf("path = %q", got)
		}
		if got := req.Header.Get("X-Xbl-Contract-Version"); got != "7" {
			t.Fatalf("contract version = %q, want 7", got)
		}
		return jsonResponse(http.StatusOK, `{"people":[{"xuid":"1","gamertag":"Player","isFollowingCaller":true}]}`), nil
	})}, nil, xsts.UserInfo{XUID: "me"}, nil)

	users, err := client.Followers(context.Background())
	if err != nil {
		t.Fatalf("Followers returned error: %v", err)
	}
	if !requested {
		t.Fatal("transport was not called")
	}
	if len(users) != 1 || users[0].XUID != "1" || !users[0].Followed {
		t.Fatalf("users = %#v", users)
	}
}

func TestFollowingQueriesPeopleHubSocial(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Path; got != "/users/me/people/social/decoration/bio,detail,multiplayerSummary,preferredColor,presenceDetail" {
			t.Fatalf("path = %q", got)
		}
		return jsonResponse(http.StatusOK, `{"people":[]}`), nil
	})}, nil, xsts.UserInfo{XUID: "me"}, nil)

	if _, err := client.Following(context.Background()); err != nil {
		t.Fatalf("Following returned error: %v", err)
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
