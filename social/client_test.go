package social

import (
	"context"
	"errors"
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

func TestAddFriendReturnsServiceError(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", req.Method)
		}
		if !strings.Contains(req.URL.Path, "/users/me/people/friends/v2/xuid(2)") {
			t.Fatalf("path = %q", req.URL.Path)
		}
		return jsonResponse(http.StatusForbidden, `{"code":1028,"description":"friend list full","source":"social"}`), nil
	})}, nil, xsts.UserInfo{XUID: "1"}, nil)

	err := client.AddFriend(context.Background(), "2")
	if err == nil {
		t.Fatal("AddFriend returned nil error")
	}
	var socialErr *ServiceError
	if !errors.As(err, &socialErr) {
		t.Fatalf("error = %T, want *ServiceError", err)
	}
	if socialErr.Code != 1028 || socialErr.Description != "friend list full" {
		t.Fatalf("service error = %#v", socialErr)
	}
	if !IsFriendListFull(err) {
		t.Fatalf("IsFriendListFull(%v) = false, want true", err)
	}
}

func TestSocialRateLimitReturnsRetryAfterError(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp := jsonResponse(http.StatusTooManyRequests, `{}`)
		resp.Header.Set("Retry-After", "7")
		return resp, nil
	})}, nil, xsts.UserInfo{XUID: "1"}, nil)

	err := client.Follow(context.Background(), "2")
	if err == nil {
		t.Fatal("Follow returned nil error")
	}
	var retryAfter *RetryAfterError
	if !errors.As(err, &retryAfter) {
		t.Fatalf("error = %T, want *RetryAfterError", err)
	}
	if retryAfter.RetryDelay() != 7*time.Second {
		t.Fatalf("retry delay = %s, want 7s", retryAfter.RetryDelay())
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
