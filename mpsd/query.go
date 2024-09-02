package mpsd

import (
	"encoding/json"
	"fmt"
	"github.com/df-mc/go-xsapi"
	"github.com/df-mc/go-xsapi/internal"
	"net/http"
	"strconv"
)

type Query struct {
	Client *http.Client
}

// Query retrieves the Commit of a session referenced in SessionReference.
func (q Query) Query(src xsapi.TokenSource, ref SessionReference) (*Commit, error) {
	if q.Client == nil {
		q.Client = &http.Client{}
	}
	internal.SetTransport(q.Client, src)

	req, err := http.NewRequest(http.MethodGet, ref.URL().String(), nil)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("X-Xbl-Contract-Version", strconv.Itoa(contractVersion))

	resp, err := q.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var c *Commit
		if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
			return nil, fmt.Errorf("decode response body: %w", err)
		}
		return c, nil
	default:
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}
