package social

import (
	"context"
	"net/http"
	"net/url"

	"github.com/df-mc/go-xsapi/internal"
)

func (c *Client) Follow(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	requestURL := socialEndpoint.JoinPath(
		"users",
		"me",
		"people",
		"xuid("+xuid+")",
	).String()
	return c.do(ctx, http.MethodPut, requestURL, nil, nil, append(
		opts,
		socialContractVersion,
	))
}

func (c *Client) Unfollow(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	return c.deleteRelationships(ctx, xuid, "follows", opts)
}

func (c *Client) AddFriend(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	requestURL := socialEndpoint.JoinPath(
		"/users/xuid(2535428765332540)/people/friends/v2",
		"xuid("+xuid+")",
	).String()
	return c.do(ctx, http.MethodPut, requestURL, nil, nil, opts)
}

func (c *Client) RemoveFriend(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	return c.deleteRelationships(ctx, xuid, "friends", opts)
}

func (c *Client) deleteRelationships(ctx context.Context, xuid, typ string, opts []internal.RequestOption) error {
	requestURL := socialEndpoint.JoinPath(
		"users",
		"me",
		"people",
		"friends",
		"v2",
		"xuid("+xuid+")",
	)
	q := requestURL.Query()
	q.Set("deleteRelationships", typ)
	requestURL.RawQuery = q.Encode()
	return c.do(ctx, http.MethodDelete, requestURL.String(), nil, nil, opts)
}

var (
	socialEndpoint = &url.URL{
		Scheme: "https",
		Host:   "social.xboxlive.com",
	}
	socialContractVersion = internal.ContractVersion("3")
)
