package achievements

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

const (
	// Resource limits bound responses and iteration even if the service keeps
	// returning new continuation tokens.
	maxPageBytes    = 4 << 20
	maxPages        = 128
	maxAchievements = 65536
)

var endpointURL = &url.URL{Scheme: "https", Host: "achievements.xboxlive.com"}

const (
	// TypePersistent selects achievements that can be unlocked at any time.
	TypePersistent = "Persistent"
	// TypeChallenge selects achievements with a limited unlock period.
	TypeChallenge = "Challenge"
)

// New returns an achievements client for the authenticated user. The HTTP client
// must authenticate requests, as [github.com/df-mc/go-xsapi/v2.Client.HTTPClient] does.
func New(client *http.Client, userInfo xsts.UserInfo) *Client {
	return &Client{client: client, userInfo: userInfo}
}

// Client reads the authenticated user's Xbox Live achievements.
type Client struct {
	client   *http.Client
	userInfo xsts.UserInfo
}

// Filter selects which achievements to return and where to start reading.
type Filter struct {
	// TitleID limits results to one title. Zero leaves the title filter unset.
	TitleID uint32
	// Type is TypePersistent or TypeChallenge. Empty includes all supported types.
	Type string
	// MaxItems requests at most this many entries per page. Zero uses the
	// service default. The value must fit in a non-negative signed 32-bit integer.
	MaxItems int
	// ContinuationToken starts at a previously returned page boundary.
	// Empty starts at the first page.
	ContinuationToken string
}

// Achievements reads one page matching filter, limited to 4 MiB per response.
// Pass the returned continuation token in filter to read the next page.
// Set language preferences with [github.com/df-mc/go-xsapi/v2.AcceptLanguage].
func (c *Client) Achievements(ctx context.Context, filter Filter, opts ...internal.RequestOption) (PageResult, error) {
	if _, err := strconv.ParseUint(c.userInfo.XUID, 10, 64); err != nil {
		return PageResult{}, fmt.Errorf("achievements: invalid user ID: %w", err)
	}
	if filter.MaxItems < 0 || filter.MaxItems > math.MaxInt32 {
		return PageResult{}, fmt.Errorf("achievements: max items must be between 0 and %d", math.MaxInt32)
	}
	requestURL := endpointURL.JoinPath("users", "xuid("+c.userInfo.XUID+")", "achievements")
	query := requestURL.Query()
	if filter.TitleID != 0 {
		query.Set("titleId", strconv.FormatUint(uint64(filter.TitleID), 10))
	}
	if filter.Type != "" {
		query.Set("types", filter.Type)
	}
	if filter.MaxItems != 0 {
		query.Set("maxItems", strconv.Itoa(filter.MaxItems))
	}
	if filter.ContinuationToken != "" {
		query.Set("continuationToken", filter.ContinuationToken)
	}
	requestURL.RawQuery = query.Encode()
	req, err := internal.NewRequest(ctx, http.MethodGet, requestURL.String(), nil, append(slices.Clip(opts),
		internal.ContractVersion("2"),
		internal.RequestHeader("Accept", "application/json"),
		internal.DefaultLanguage,
	))
	if err != nil {
		return PageResult{}, fmt.Errorf("make request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return PageResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return PageResult{}, internal.UnexpectedStatusCode(resp)
	}
	// MaxItems limits the requested entry count, not the size of descriptions
	// or other fields. Bound bytes locally even if the service ignores that filter.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return PageResult{}, fmt.Errorf("read achievements: %w", err)
	}
	if len(body) > maxPageBytes {
		return PageResult{}, errors.New("achievements: response exceeds 4 MiB")
	}
	var page PageResult
	if err := json.Unmarshal(body, &page); err != nil {
		return PageResult{}, fmt.Errorf("decode achievements: %w", err)
	}
	if page.Achievements == nil {
		return PageResult{}, errors.New("achievements: response has no collection")
	}
	return page, nil
}

// All reads matching achievements from filter's starting page in service order.
// It keeps the first occurrence of each service configuration ID and achievement
// ID pair and skips entries without an ID. It returns no partial results on errors,
// including repeated continuation tokens, over 128 pages, or over 65,536 unique
// achievements. Language preferences may be supplied with
// [github.com/df-mc/go-xsapi/v2.AcceptLanguage].
func (c *Client) All(ctx context.Context, filter Filter, opts ...internal.RequestOption) ([]Achievement, error) {
	result := []Achievement{}
	seen := make(map[[2]string]bool)
	tokens := make(map[string]bool)
	if filter.ContinuationToken != "" {
		tokens[filter.ContinuationToken] = true
	}
	for range maxPages {
		page, err := c.Achievements(ctx, filter, opts...)
		if err != nil {
			return nil, err
		}
		for _, value := range page.Achievements {
			key := [2]string{value.ServiceConfigID, value.ID}
			if value.ID == "" || seen[key] {
				continue
			}
			if len(result) == maxAchievements {
				return nil, errors.New("achievements: collection exceeds 65,536 entries")
			}
			seen[key] = true
			result = append(result, value)
		}
		next := page.PagingInfo.ContinuationToken
		if next == "" {
			return result, nil
		}
		if tokens[next] {
			return nil, errors.New("achievements: continuation did not advance")
		}
		tokens[next] = true
		filter.ContinuationToken = next
	}
	return nil, errors.New("achievements: pagination exceeds 128 pages")
}
