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

// IsFriendListFull reports whether err indicates a friend-list capacity limit.
func IsFriendListFull(err error) bool {
	var serviceErr *ServiceError
	return errors.As(err, &serviceErr) && serviceErr.Code == 1028
}

// IsFriendRestricted reports whether err indicates a privacy, policy, or
// relationship restriction for a friend-management operation.
func IsFriendRestricted(err error) bool {
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) {
		return false
	}
	switch serviceErr.Code {
	case 1011, 1049:
		return true
	default:
		return false
	}
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
	}
}
