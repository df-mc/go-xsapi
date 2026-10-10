package achievements

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

const (
	endpoint = "https://achievements.xboxlive.com"
	// Resource limits bound responses and iteration even if the service keeps
	// returning new continuation tokens.
	maxPageBytes    = 4 << 20
	maxPages        = 128
	maxAchievements = 65536
)

// New returns an achievements client for the authenticated user. The HTTP client
// must authenticate requests, as [github.com/df-mc/go-xsapi/v2.Client.HTTPClient] does.
func New(client *http.Client, userInfo xsts.UserInfo) *Client {
	return &Client{client: client, xuid: userInfo.XUID}
}

// Client reads the authenticated user's Xbox Live achievements.
type Client struct {
	client *http.Client
	xuid   string
}

// ForTitlePage reads one page for titleID using the v2 achievements contract.
// Pass an empty continuationToken for the first page, then use the returned
// [PagingInfo.ContinuationToken] for subsequent pages. Each response is limited
// to 4 MiB. Language preferences may be supplied with
// [github.com/df-mc/go-xsapi/v2.AcceptLanguage].
func (c *Client) ForTitlePage(ctx context.Context, titleID uint32, continuationToken string, opts ...internal.RequestOption) (Page, error) {
	if _, err := strconv.ParseUint(c.xuid, 10, 64); err != nil {
		return Page{}, fmt.Errorf("achievements: invalid user ID: %w", err)
	}
	query := url.Values{"titleId": {strconv.FormatUint(uint64(titleID), 10)}}
	if continuationToken != "" {
		query.Set("continuationToken", continuationToken)
	}
	req, err := internal.NewRequest(ctx, http.MethodGet, endpoint+"/users/xuid("+c.xuid+")/achievements?"+query.Encode(), nil, append(slices.Clip(opts),
		internal.ContractVersion("2"),
		internal.RequestHeader("Accept", "application/json"),
		internal.DefaultLanguage,
	))
	if err != nil {
		return Page{}, fmt.Errorf("make request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Page{}, internal.UnexpectedStatusCode(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return Page{}, fmt.Errorf("read achievements: %w", err)
	}
	if len(body) > maxPageBytes {
		return Page{}, fmt.Errorf("achievement response exceeds limit")
	}
	var page Page
	if err := json.Unmarshal(body, &page); err != nil {
		return Page{}, fmt.Errorf("decode achievements: %w", err)
	}
	if page.Achievements == nil {
		return Page{}, fmt.Errorf("achievement response has no collection")
	}
	return page, nil
}

// ForTitle reads all pages for titleID in service order. It keeps the first
// occurrence of each service configuration ID and achievement ID pair and skips
// entries without an ID. It returns an error, without partial results, for
// repeated continuation tokens, more than 128 pages, or more than 65,536 unique
// achievements. Language preferences may be supplied with
// [github.com/df-mc/go-xsapi/v2.AcceptLanguage].
func (c *Client) ForTitle(ctx context.Context, titleID uint32, opts ...internal.RequestOption) ([]Achievement, error) {
	result := []Achievement{}
	seen := make(map[[2]string]bool)
	tokens := make(map[string]bool)
	continuation := ""
	for range maxPages {
		page, err := c.ForTitlePage(ctx, titleID, continuation, opts...)
		if err != nil {
			return nil, err
		}
		for _, value := range page.Achievements {
			key := [2]string{value.ServiceConfigID, value.ID}
			if value.ID == "" || seen[key] {
				continue
			}
			if len(result) == maxAchievements {
				return nil, fmt.Errorf("achievement collection exceeds limit")
			}
			seen[key] = true
			result = append(result, value)
		}
		next := page.PagingInfo.ContinuationToken
		if next == "" {
			return result, nil
		}
		if tokens[next] {
			return nil, fmt.Errorf("achievement continuation did not advance")
		}
		tokens[next] = true
		continuation = next
	}
	return nil, fmt.Errorf("achievement pagination exceeds limit")
}
