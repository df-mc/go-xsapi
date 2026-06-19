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

func TestFollowReturnsResponseError(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		headers     http.Header
		body        string
		code        int
		description string
		source      string
		kind        string
		retryAfter  time.Duration
	}{
		{
			name:       "rate limit retry after seconds",
			statusCode: http.StatusTooManyRequests,
			headers: http.Header{
				"Retry-After": []string{"7"},
			},
			kind:       FriendErrorKindUnknown,
			retryAfter: 7 * time.Second,
		},
		{
			name:        "friend list full",
			statusCode:  http.StatusBadRequest,
			body:        `{"code":1028,"description":"The attempted People request was rejected because it would exceed the People list limit.","source":"people"}`,
			code:        1028,
			description: "The attempted People request was rejected because it would exceed the People list limit.",
			source:      "people",
			kind:        FriendErrorKindFullList,
		},
		{
			name:        "restricted relationship",
			statusCode:  http.StatusBadRequest,
			body:        `{"code":1049,"description":"Target user privacy settings do not allow friend requests to be received."}`,
			code:        1049,
			description: "Target user privacy settings do not allow friend requests to be received.",
			kind:        FriendErrorKindRestricted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := New(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPut {
					t.Fatalf("request method = %s, want PUT", req.Method)
				}
				resp := response(req, tt.statusCode, tt.body)
				for key, values := range tt.headers {
					for _, value := range values {
						resp.Header.Add(key, value)
					}
				}
				return resp, nil
			})}, nil, xsts.UserInfo{}, nil)

			err := client.Follow(context.Background(), "123")
			if err == nil {
				t.Fatal("Follow returned nil error")
			}
			var responseErr *ResponseError
			if !errors.As(err, &responseErr) {
				t.Fatalf("Follow error = %T: %v, want *ResponseError", err, err)
			}
			if responseErr.StatusCode != tt.statusCode {
				t.Fatalf("StatusCode = %d, want %d", responseErr.StatusCode, tt.statusCode)
			}
			if responseErr.Method != http.MethodPut {
				t.Fatalf("Method = %q, want PUT", responseErr.Method)
			}
			if !strings.Contains(responseErr.URL, "/users/me/people/xuid(123)") {
				t.Fatalf("URL = %q, want follow URL", responseErr.URL)
			}
			if responseErr.Code != tt.code {
				t.Fatalf("Code = %d, want %d", responseErr.Code, tt.code)
			}
			if responseErr.Description != tt.description {
				t.Fatalf("Description = %q, want %q", responseErr.Description, tt.description)
			}
			if responseErr.Source != tt.source {
				t.Fatalf("Source = %q, want %q", responseErr.Source, tt.source)
			}
			if responseErr.FriendErrorKind() != tt.kind {
				t.Fatalf("FriendErrorKind = %q, want %q", responseErr.FriendErrorKind(), tt.kind)
			}
			if responseErr.RetryDelay() != tt.retryAfter {
				t.Fatalf("RetryDelay = %s, want %s", responseErr.RetryDelay(), tt.retryAfter)
			}
		})
	}
}

func TestSocialErrorClassificationHelpers(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		rateLimit  bool
		fullList   bool
		restricted bool
	}{
		{
			name:      "rate limited",
			err:       &ResponseError{StatusCode: http.StatusTooManyRequests},
			rateLimit: true,
		},
		{
			name:     "friend list full",
			err:      &ResponseError{Code: 1028},
			fullList: true,
		},
		{
			name:       "restricted",
			err:        &ResponseError{Code: 1011},
			restricted: true,
		},
		{
			name: "unrelated",
			err:  errors.New("plain error"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsRateLimited(tt.err) != tt.rateLimit {
				t.Fatalf("IsRateLimited = %t, want %t", IsRateLimited(tt.err), tt.rateLimit)
			}
			if IsFriendListFull(tt.err) != tt.fullList {
				t.Fatalf("IsFriendListFull = %t, want %t", IsFriendListFull(tt.err), tt.fullList)
			}
			if IsFriendRestricted(tt.err) != tt.restricted {
				t.Fatalf("IsFriendRestricted = %t, want %t", IsFriendRestricted(tt.err), tt.restricted)
			}
		})
	}
}

func TestUnfollowReturnsResponseError(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodDelete {
			t.Fatalf("request method = %s, want DELETE", req.Method)
		}
		return response(req, http.StatusBadRequest, `{"code":1028,"description":"full"}`), nil
	})}, nil, xsts.UserInfo{}, nil)

	err := client.Unfollow(context.Background(), "123")
	if err == nil {
		t.Fatal("Unfollow returned nil error")
	}
	if !IsFriendListFull(err) {
		t.Fatalf("IsFriendListFull = false for %T: %v", err, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func response(req *http.Request, statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
