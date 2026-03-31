package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// XBLRelyingParty is the relying party used for various Xbox Live services.
// In XSAPI Client, it will be used for requesting NSAL endpoints for current
// authenticated title.
const XBLRelyingParty = "http://xboxlive.com"

// NewRequest creates a new HTTP request with the given context, method, URL, and body,
// then applies any provided request options.
func NewRequest(ctx context.Context, method, u string, reqBody io.Reader, opts []RequestOption) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, err
	}
	Apply(req, opts)
	return req, nil
}

// WithJSONBody creates a new HTTP request with the given body JSON-encoded as the request body.
func WithJSONBody(ctx context.Context, method, u string, reqBody any, opts []RequestOption) (*http.Request, error) {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}
	return NewRequest(ctx, method, u, buf, opts)
}

// DecodeJSON decodes the contents of r into a value of type T.
func DecodeJSON[T any](r io.Reader) (value T, err error) {
	if err := json.NewDecoder(r).Decode(&value); err != nil {
		return value, err
	}
	return value, nil
}

// UnexpectedStatusCode returns an error describing an unexpected HTTP status code,
// including the request method and URL for context.
// The resp must be a client response because [http.Response.Request] is only
// populated on responses received by the client.
func UnexpectedStatusCode(resp *http.Response) error {
	return fmt.Errorf("%s %s: %s", resp.Request.Method, resp.Request.URL, resp.Status)
}

// Do sends an HTTP request to the given URL using the provided client.
//
// If reqBody is non-nil, it is JSON-encoded and sent as the request body with
// the 'Content-Type' header set to 'application/json'.
//
// If respBody is non-nil and the response status is 200 OK or 201 Created,
// the response body is JSON-decoded into respBody.
//
// opts are applied to the request before it is sent. Useful options are defined in this package.
func Do(ctx context.Context, client *http.Client, method, u string, reqBody, respBody any, opts []RequestOption) error {
	var r io.Reader
	if reqBody != nil {
		buf := &bytes.Buffer{}
		defer buf.Reset()
		if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		r = buf
	}

	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return fmt.Errorf("make request: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	Apply(req, opts)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if respBody != nil {
			if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
				return fmt.Errorf("decode response body: %w", err)
			}
		}
		return nil
	default:
		return UnexpectedStatusCode(resp)
	}
}
