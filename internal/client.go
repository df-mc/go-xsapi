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

func NewRequest(ctx context.Context, method, u string, reqBody io.Reader, opts []RequestOption) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, err
	}
	Apply(req, opts)
	return req, nil
}

func Do(ctx context.Context, client *http.Client, method, u string, reqBody, respBody any, opts []RequestOption) error {
	var r io.Reader
	if reqBody != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		defer buf.Reset()
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
		return fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}
