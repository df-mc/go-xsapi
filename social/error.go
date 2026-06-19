package social

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	FriendErrorKindUnknown    = "unknown"
	FriendErrorKindFullList   = "friend_list_full"
	FriendErrorKindRestricted = "restricted"
)

// Social API error categories for use with errors.Is.
var (
	ErrRateLimited      = errors.New("xsapi/social: rate limited")
	ErrFriendListFull   = errors.New("xsapi/social: friend list full")
	ErrFriendRestricted = errors.New("xsapi/social: friend restricted")
)

// ResponseError carries error details returned by the Xbox Live Social and
// PeopleHub APIs.
type ResponseError struct {
	// Method is the HTTP request method, if available.
	Method string
	// URL is the HTTP request URL, if available.
	URL string
	// StatusCode is the HTTP response status code.
	StatusCode int
	// Code is the Xbox social service error code, if the response body included one.
	Code int
	// Description is the Xbox social service error description, if present.
	Description string
	// Source is the Xbox social service error source, if present.
	Source string
	// RetryAfter is the server-requested delay before retrying, if present.
	RetryAfter time.Duration
}

func (e *ResponseError) Error() string {
	prefix := ""
	if e.Method != "" && e.URL != "" {
		prefix = e.Method + " " + e.URL + ": "
	}
	if e.Code != 0 && e.Description != "" {
		return fmt.Sprintf("%sxsapi/social: request failed: status=%d code=%d description=%q", prefix, e.StatusCode, e.Code, e.Description)
	}
	if e.Code != 0 {
		return fmt.Sprintf("%sxsapi/social: request failed: status=%d code=%d", prefix, e.StatusCode, e.Code)
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%sxsapi/social: request failed: status=%d retry_after=%s", prefix, e.StatusCode, e.RetryAfter)
	}
	return fmt.Sprintf("%sxsapi/social: request failed: status=%d", prefix, e.StatusCode)
}

// Is supports errors.Is checks against the Social API error categories.
func (e *ResponseError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrRateLimited:
		return e.StatusCode == http.StatusTooManyRequests || e.RetryAfter > 0
	case ErrFriendListFull:
		return e.FriendErrorKind() == FriendErrorKindFullList
	case ErrFriendRestricted:
		return e.FriendErrorKind() == FriendErrorKindRestricted
	default:
		return false
	}
}

// RetryDelay returns RetryAfter for callers that use a small retry interface
// with errors.As.
func (e *ResponseError) RetryDelay() time.Duration {
	if e == nil {
		return 0
	}
	return e.RetryAfter
}

// XboxSocialCode returns the Xbox social service error code, if present.
func (e *ResponseError) XboxSocialCode() int {
	if e == nil {
		return 0
	}
	return e.Code
}

// FriendErrorKind returns the best-known classification for the Xbox social
// service error code. Unknown or unclassified codes return
// FriendErrorKindUnknown.
func (e *ResponseError) FriendErrorKind() string {
	if e == nil {
		return FriendErrorKindUnknown
	}
	return classifyFriendErrorCode(e.Code)
}

func responseError(resp *http.Response) error {
	responseErr := &ResponseError{
		StatusCode: resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}
	if resp.Request != nil {
		responseErr.Method = resp.Request.Method
		if resp.Request.URL != nil {
			responseErr.URL = resp.Request.URL.String()
		}
	}
	if resp.Body == nil {
		return responseErr
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("xsapi/social: read error response: %w", err)
	}
	var data struct {
		Code        int    `json:"code"`
		Description string `json:"description"`
		Source      string `json:"source"`
	}
	if err := json.Unmarshal(body, &data); err == nil {
		responseErr.Code = data.Code
		responseErr.Description = data.Description
		responseErr.Source = data.Source
	}
	return responseErr
}

func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	delay := time.Until(when)
	if delay < 0 {
		return 0
	}
	return delay
}

func classifyFriendErrorCode(code int) string {
	switch code {
	case 1028:
		return FriendErrorKindFullList
	case 1011, 1049:
		return FriendErrorKindRestricted
	default:
		return FriendErrorKindUnknown
	}
}
