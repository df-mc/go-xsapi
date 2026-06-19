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
	// FriendErrorKindUnknown indicates that an Xbox social error could not be
	// mapped to a more specific friend-management category.
	FriendErrorKindUnknown = "unknown"
	// FriendErrorKindFullList indicates that a friend relationship could not be
	// created because one of the users has reached a friend-list limit.
	FriendErrorKindFullList = "friend_list_full"
	// FriendErrorKindRestricted indicates that privacy, policy, or relationship
	// restrictions prevented the requested friend-management operation.
	FriendErrorKindRestricted = "restricted"
)

// RetryAfterError reports that Xbox Live rejected a social request with a
// retryable rate limit response.
type RetryAfterError struct {
	Method     string
	URL        string
	StatusCode int
	Delay      time.Duration
}

func (e *RetryAfterError) Error() string {
	if e.Delay <= 0 {
		return fmt.Sprintf("%s %s: rate limited", e.Method, e.URL)
	}
	return fmt.Sprintf("%s %s: rate limited, retry after %s", e.Method, e.URL, e.Delay)
}

// RetryDelay returns the delay requested by the Retry-After response header.
func (e *RetryAfterError) RetryDelay() time.Duration {
	return e.Delay
}

// ServiceError reports a non-success response returned by an Xbox social or
// PeopleHub endpoint.
type ServiceError struct {
	Method      string
	URL         string
	StatusCode  int
	Code        int
	Description string
	Source      string
	Kind        string
}

func (e *ServiceError) Error() string {
	if e.Code == 0 {
		return fmt.Sprintf("%s %s: %d", e.Method, e.URL, e.StatusCode)
	}
	if e.Description == "" {
		return fmt.Sprintf("%s %s: xbox social code %d", e.Method, e.URL, e.Code)
	}
	return fmt.Sprintf("%s %s: xbox social code %d: %s", e.Method, e.URL, e.Code, e.Description)
}

// XboxSocialCode returns the integer code from the Xbox social error payload.
func (e *ServiceError) XboxSocialCode() int {
	return e.Code
}

// FriendErrorKind returns the friend-management category for this social error.
func (e *ServiceError) FriendErrorKind() string {
	if e.Kind == "" {
		return FriendErrorKindUnknown
	}
	return e.Kind
}

// IsFriendListFull reports whether err indicates a friend-list capacity limit.
func IsFriendListFull(err error) bool {
	var social interface {
		FriendErrorKind() string
	}
	return errors.As(err, &social) && social.FriendErrorKind() == FriendErrorKindFullList
}

// IsFriendRestricted reports whether err indicates a privacy, policy, or
// relationship restriction for a friend-management operation.
func IsFriendRestricted(err error) bool {
	var social interface {
		FriendErrorKind() string
	}
	return errors.As(err, &social) && social.FriendErrorKind() == FriendErrorKindRestricted
}

func responseError(req *http.Request, resp *http.Response) error {
	if resp.StatusCode == http.StatusTooManyRequests {
		return &RetryAfterError{
			Method:     req.Method,
			URL:        req.URL.String(),
			StatusCode: resp.StatusCode,
			Delay:      parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%s %s: %s: read error body: %w", req.Method, req.URL, resp.Status, err)
	}
	return decodeServiceError(req, resp, body)
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

func decodeServiceError(req *http.Request, resp *http.Response, body []byte) *ServiceError {
	var data struct {
		Code        int    `json:"code"`
		Description string `json:"description"`
		Source      string `json:"source"`
	}
	_ = json.Unmarshal(body, &data)
	return &ServiceError{
		Method:      req.Method,
		URL:         req.URL.String(),
		StatusCode:  resp.StatusCode,
		Code:        data.Code,
		Description: data.Description,
		Source:      data.Source,
		Kind:        classifyFriendSocialCode(data.Code),
	}
}

func classifyFriendSocialCode(code int) string {
	switch code {
	case 1028:
		return FriendErrorKindFullList
	case 1011, 1049:
		return FriendErrorKindRestricted
	default:
		return FriendErrorKindUnknown
	}
}
