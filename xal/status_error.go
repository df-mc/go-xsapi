package xal

import (
	"fmt"
	"net/http"
)

// StatusError reports an unexpected HTTP response status from an Xbox Live
// authentication request.
type StatusError struct {
	Method     string
	URL        string
	Status     string
	StatusCode int
}

func (e *StatusError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s %s: %s", e.Method, e.URL, e.Status)
}

// UnexpectedStatus returns a StatusError for resp.
func UnexpectedStatus(resp *http.Response) *StatusError {
	err := &StatusError{
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
	}
	if resp.Request != nil {
		err.Method = resp.Request.Method
		if resp.Request.URL != nil {
			err.URL = resp.Request.URL.String()
		}
	}
	return err
}
