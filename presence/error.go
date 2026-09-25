package presence

import (
	"fmt"
	"net/http"
	"time"

	"github.com/df-mc/go-xsapi/v2/internal"
)

// ResponseError is returned when the Presence API answers with an unexpected
// status code.
type ResponseError struct {
	// Method and URL identify the failed request.
	Method, URL string
	// Status is the HTTP status line, such as "429 Too Many Requests".
	Status string
	// StatusCode is the HTTP response status code.
	StatusCode int
	// RetryAfter is the server-requested delay before retrying, if present.
	RetryAfter time.Duration
}

// Error implements error in the same form as other unexpected status errors.
func (e *ResponseError) Error() string {
	msg := fmt.Sprintf("%s %s: %s", e.Method, e.URL, e.Status)
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	return msg
}

// responseError builds a ResponseError from an unsuccessful client response.
func responseError(resp *http.Response) error {
	return &ResponseError{
		Method:     resp.Request.Method,
		URL:        resp.Request.URL.String(),
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		RetryAfter: internal.ParseRetryAfter(resp.Header.Get("Retry-After")),
	}
}
