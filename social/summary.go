package social

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/df-mc/go-xsapi/v2/internal"
)

// Summary holds the social summary of a user, describing follower and
// following totals and the relationship between the caller and the target.
type Summary struct {
	// TargetFollowingCount is the number of users the target user follows.
	// For the caller's own summary this counts towards the friend limit.
	TargetFollowingCount int `json:"targetFollowingCount"`
	// TargetFollowerCount is the number of users following the target user.
	TargetFollowerCount int `json:"targetFollowerCount"`
	// IsCallerFollowingTarget reports whether the caller follows the target user.
	IsCallerFollowingTarget bool `json:"isCallerFollowingTarget"`
	// IsTargetFollowingCaller reports whether the target user follows the caller.
	IsTargetFollowingCaller bool `json:"isTargetFollowingCaller"`
	// HasCallerMarkedTargetAsFavorite reports whether the caller has marked the
	// target user as a favourite.
	HasCallerMarkedTargetAsFavorite bool `json:"hasCallerMarkedTargetAsFavorite"`
	// HasCallerMarkedTargetAsKnown reports whether the caller has marked the
	// target user as known.
	HasCallerMarkedTargetAsKnown bool `json:"hasCallerMarkedTargetAsKnown"`
	// LegacyFriendStatus describes the pre-follow-model relationship between
	// the caller and the target. Known values include "None".
	LegacyFriendStatus string `json:"legacyFriendStatus"`
	// XUID is the XUID of the target user.
	XUID string `json:"xuid"`
}

// Summary returns the caller's own social summary, including how many users
// the caller follows out of the friend limit.
func (c *Client) Summary(ctx context.Context, opts ...internal.RequestOption) (Summary, error) {
	return c.summary(ctx, "me", opts)
}

// SummaryOf returns the social summary of the user identified by the given
// XUID from the caller's perspective.
func (c *Client) SummaryOf(ctx context.Context, xuid string, opts ...internal.RequestOption) (Summary, error) {
	return c.summary(ctx, "xuid("+xuid+")", opts)
}

// summary fetches the social summary for the given perspective, which is
// either "me" or a "xuid(...)" selector.
func (c *Client) summary(ctx context.Context, perspective string, opts []internal.RequestOption) (Summary, error) {
	requestURL := socialEndpoint.JoinPath(
		"users",
		perspective,
		"summary",
	).String()

	req, err := internal.NewRequest(ctx, http.MethodGet, requestURL, nil, append(
		opts,
		socialContractVersion,
		internal.RequestHeader("Accept", "application/json"),
		internal.DefaultLanguage,
	))
	if err != nil {
		return Summary{}, fmt.Errorf("make request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return Summary{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var summary Summary
		if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
			return Summary{}, fmt.Errorf("decode response body: %w", err)
		}
		return summary, nil
	default:
		return Summary{}, responseError(resp)
	}
}
