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
		name       string
		statusCode int
		headers    http.Header
		body       string
		assert     func(*testing.T, error, *ResponseError)
	}{
		{
			name:       "rate limited",
			statusCode: http.StatusTooManyRequests,
			headers: http.Header{
				"Retry-After": []string{"7"},
			},
			assert: func(t *testing.T, err error, responseErr *ResponseError) {
				t.Helper()
				if !errors.Is(err, ErrRateLimited) {
					t.Fatal("error does not match ErrRateLimited")
				}
				if responseErr.RetryAfter != 7*time.Second {
					t.Fatalf("RetryAfter = %s, want 7s", responseErr.RetryAfter)
				}
			},
		},
		{
			name:       "service code",
			statusCode: http.StatusBadRequest,
			body:       `{"code":1028,"description":"full","source":"people"}`,
			assert: func(t *testing.T, err error, responseErr *ResponseError) {
				t.Helper()
				if !errors.Is(err, ErrFriendListFull) {
					t.Fatal("error does not match ErrFriendListFull")
				}
				if responseErr.Code != 1028 || responseErr.Description != "full" || responseErr.Source != "people" {
					t.Fatalf("response error = %+v", responseErr)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := New(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
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
			tt.assert(t, err, responseErr)
		})
	}
}

func TestResponseErrorMatchesCategories(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		target error
	}{
		{name: "rate limited", err: &ResponseError{StatusCode: http.StatusTooManyRequests}, target: ErrRateLimited},
		{name: "friend list full", err: &ResponseError{Code: 1028}, target: ErrFriendListFull},
		{name: "restricted", err: &ResponseError{Code: 1011}, target: ErrFriendRestricted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, tt.target) {
				t.Fatalf("errors.Is(%v) = false", tt.target)
			}
		})
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
