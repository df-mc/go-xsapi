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

func TestAddFriendReturnsFriendListFull(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("Method = %q, want PUT", req.Method)
		}
		if !strings.Contains(req.URL.Path, "/users/me/people/friends/v2/xuid(123)") {
			t.Fatalf("Path = %q, want AddFriend path", req.URL.Path)
		}
		return response(req, http.StatusBadRequest, `{"code":1028,"description":"full"}`), nil
	})}, nil, xsts.UserInfo{}, nil)

	err := client.AddFriend(context.Background(), "123")
	if err == nil {
		t.Fatal("AddFriend returned nil error")
	}
	if !errors.Is(err, ErrFriendListFull) {
		t.Fatalf("errors.Is(ErrFriendListFull) = false for %T: %v", err, err)
	}
}

func TestResponseErrorMatchesCategories(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{name: "rate limited", err: &ResponseError{StatusCode: http.StatusTooManyRequests}, target: ErrRateLimited, want: true},
		{name: "retry after without rate limit", err: &ResponseError{StatusCode: http.StatusInternalServerError, RetryAfter: time.Second}, target: ErrRateLimited},
		{name: "friend list full", err: &ResponseError{Code: 1028}, target: ErrFriendListFull, want: true},
		{name: "restricted", err: &ResponseError{Code: 1011}, target: ErrFriendRestricted, want: true},
		{name: "restricted alternate", err: &ResponseError{Code: 1049}, target: ErrFriendRestricted, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if errors.Is(tt.err, tt.target) != tt.want {
				t.Fatalf("errors.Is(%v) = %t, want %t", tt.target, errors.Is(tt.err, tt.target), tt.want)
			}
		})
	}
}

func TestResponseErrorPreservesMetadataWhenBodyReadFails(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://peoplehub.xboxlive.com/users/me/people/social", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header: http.Header{
			"Retry-After": []string{"7"},
		},
		Body:    errReadCloser{},
		Request: req,
	}

	err = responseError(resp)
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) {
		t.Fatalf("responseError = %T: %v, want *ResponseError", err, err)
	}
	if responseErr.StatusCode != http.StatusTooManyRequests || responseErr.RetryAfter != 7*time.Second {
		t.Fatalf("response error = %+v", responseErr)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	delay := parseRetryAfter(time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	if delay <= 0 || delay > time.Hour {
		t.Fatalf("delay = %s, want within the next hour", delay)
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

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func (errReadCloser) Close() error {
	return nil
}
