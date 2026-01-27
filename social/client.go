package social

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/df-mc/go-xsapi/rta"
)

func New(api API) *Client {
	return &Client{
		api: api,
	}
}

type API interface {
	rta.Provider
	internal.HTTPClient
}

type Client struct {
	api API
}

func (c *Client) do(ctx context.Context, method, u string, reqBody, respBody any) error {
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
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if respBody != nil {
		req.Header.Set("User-Agent", "XboxServicesAPI/2024.03.20240404.1 c")
	}
	req.Header.Set("x-xbl-contract-version", "2")

	resp, err := c.api.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		if respBody != nil {
			if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
				return fmt.Errorf("decode response body: %w", err)
			}
		}
		return nil
	default:
		return fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}
