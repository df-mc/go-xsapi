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
	// Observed Social REST response body codes. These are not xsapi HRESULT
	// values and are not documented in the GDK headers.
	socialCodeFriendListFull    = 1028 // Observed when the People list limit would be exceeded.
	socialCodeRestricted        = 1011 // Observed for forbidden relationship operations.
	socialCodeRestrictedPrivacy = 1049 // Observed for target-user privacy restrictions.
)

var (
	// ErrRateLimited matches responses that indicate the caller should wait before retrying.
	ErrRateLimited = errors.New("xsapi/social: rate limited")
	// ErrFriendListFull matches responses caused by caller or target friend-list limits.
	ErrFriendListFull = errors.New("xsapi/social: friend list full")
	// ErrFriendRestricted matches privacy, enforcement, or relationship restriction responses.
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

// Error implements error by formatting e as a Social API response failure.
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

// Is implements errors.Is matching for ErrRateLimited, ErrFriendListFull, and
// ErrFriendRestricted.
func (e *ResponseError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrRateLimited:
		return e.StatusCode == http.StatusTooManyRequests
	case ErrFriendListFull:
		return e.Code == socialCodeFriendListFull
	case ErrFriendRestricted:
		return e.Code == socialCodeRestricted || e.Code == socialCodeRestrictedPrivacy
	default:
		return false
	}
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
		return responseErr
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
