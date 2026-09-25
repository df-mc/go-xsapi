package social

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/df-mc/go-xsapi/v2/internal"
)

const (
	socialCodeFriendListFull     = 1028 // Observed when the People list limit would be exceeded.
	socialCodeRestricted         = 1011 // Observed for forbidden relationship operations.
	socialCodeRestrictedPrivacy  = 1049 // Observed for target-user privacy restrictions.
	socialCodeBulkOperationLimit = 1050 // Observed when a bulk relationship request contains too many users.

	maxResponseErrorBody = 512 // Bytes of an uncoded error body kept in ResponseError.Body.
)

var (
	// ErrRateLimited matches responses that indicate the caller should wait before retrying.
	ErrRateLimited = errors.New("xsapi/social: rate limited")
	// ErrFriendListFull matches responses caused by caller or target friend-list limits.
	ErrFriendListFull = errors.New("xsapi/social: friend list full")
	// ErrFriendRestricted matches privacy, enforcement, or relationship restriction responses.
	ErrFriendRestricted = errors.New("xsapi/social: friend restricted")
	// ErrBulkOperationLimit matches responses indicating a bulk relationship
	// request contains more users than the service accepts.
	ErrBulkOperationLimit = errors.New("xsapi/social: bulk operation limit")
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
	// Body is the start of the response body when it carried no service error
	// code, so otherwise opaque failures can still be diagnosed.
	Body string
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
	msg := fmt.Sprintf("%sxsapi/social: request failed: status=%d", prefix, e.StatusCode)
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf(" retry_after=%s", e.RetryAfter)
	}
	if e.Body != "" {
		msg += fmt.Sprintf(" body=%q", e.Body)
	}
	return msg
}

// Is implements errors.Is matching for Social API error categories.
func (e *ResponseError) Is(target error) bool {
	switch target {
	case ErrRateLimited:
		return e.StatusCode == http.StatusTooManyRequests
	case ErrFriendListFull:
		return e.Code == socialCodeFriendListFull
	case ErrFriendRestricted:
		return e.Code == socialCodeRestricted || e.Code == socialCodeRestrictedPrivacy
	case ErrBulkOperationLimit:
		return e.Code == socialCodeBulkOperationLimit
	default:
		return false
	}
}

// responseError builds a ResponseError from an unsuccessful Social or PeopleHub
// response.
func responseError(resp *http.Response) error {
	responseErr := &ResponseError{
		StatusCode: resp.StatusCode,
		RetryAfter: internal.ParseRetryAfter(resp.Header.Get("Retry-After")),
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
	if err := json.Unmarshal(body, &data); err == nil && data.Code != 0 {
		responseErr.Code = data.Code
		responseErr.Description = data.Description
		responseErr.Source = data.Source
		return responseErr
	}
	body = bytes.TrimSpace(body)
	if len(body) > maxResponseErrorBody {
		body = body[:maxResponseErrorBody]
	}
	responseErr.Body = strings.ToValidUTF8(string(body), "")
	return responseErr
}
