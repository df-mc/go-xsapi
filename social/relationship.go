package social

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/df-mc/go-xsapi/internal"
)

// Follow establishes a one-way follow relationship with the user identified
// by XUID.
//
// A follow relationship is applied immediately and does not require approval
// from the target user. Upon success, the target user's follower count is
// updated accordingly.
func (c *Client) Follow(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	requestURL := socialEndpoint.JoinPath(
		"users",
		"me",
		"people",
		"xuid("+xuid+")",
	).String()

	// Unlike [Client.AddFriend], this request call returns 204 No Content.
	req, err := internal.NewRequest(ctx, http.MethodPut, requestURL, nil, append(
		opts,
		socialContractVersion,
		internal.RequestHeader("Accept", "application/json"),
		internal.RequestHeader("Cache-Control", "no-cache"),
		internal.DefaultLanguage,
	))
	if err != nil {
		return fmt.Errorf("make request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	default:
		return internal.UnexpectedStatusCode(resp)
	}
}

// Unfollow removes an existing follow relationship with the user identified by XUID.
// If no follow relationship exists, an error will be returned. Therefore, it is recommended
// to check if the caller has an existing follow relationship with [Client.UserByXUID].
func (c *Client) Unfollow(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	return c.deleteRelationships(ctx, xuid, "follows", opts)
}

// AddFriend creates or accepts a friend relationship with the user identified
// by XUID.
//
// Friend relationships are mutual. If no pending request exists,
// this call sends a friend request. If the target user has already
// sent a request to the caller, this call accepts it and establishes
// the friendship.
func (c *Client) AddFriend(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	requestURL := socialEndpoint.JoinPath(
		"/users/me/people/friends/v2",
		"xuid("+xuid+")",
	).String()

	// Expected status code: 200 OK
	return internal.Do(ctx, c.client, http.MethodPut, requestURL, nil, nil, append(
		opts,
		socialContractVersion,
		internal.RequestHeader("Accept", "application/json"),
		internal.RequestHeader("Cache-Control", "no-cache"),
		internal.DefaultLanguage,
	))
}

// RemoveFriend deletes the friend relationship with the user identified by XUID.
//
// If a pending friend request exists (either sent or received), it will be
// canceled and the other user will no longer be able to accept it. A friend
// request can also be sent after this call.
//
// If the users are already friends, the friendship is terminated. To become
// friends again, a new friend request must be sent and approved.
func (c *Client) RemoveFriend(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	return c.deleteRelationships(ctx, xuid, "friends", opts)
}

// deleteRelationships removes a specific type of relationship with the user
// identified by XUID. The relationships can be "friends" or "follows".
func (c *Client) deleteRelationships(ctx context.Context, xuid, relationships string, opts []internal.RequestOption) error {
	requestURL := socialEndpoint.JoinPath(
		"/users/me/people/friends/v2",
		"xuid("+xuid+")",
	)
	q := requestURL.Query()
	q.Set("deleteRelationships", relationships)
	requestURL.RawQuery = q.Encode()

	// This request is a DELETE call but returns 200 OK instead of 204 No Content.
	return internal.Do(ctx, c.client, http.MethodDelete, requestURL.String(), nil, nil, append(
		opts,
		socialContractVersion,
		internal.RequestHeader("Accept", "application/json"),
		internal.RequestHeader("Cache-Control", "no-cache"),
		internal.DefaultLanguage,
	))
}

var (
	// socialEndpoint is the base URL used to make requests to the Xbox Live Social API.
	//
	// The Social API is primarily used to manage user relationships,
	// such as friends and follows. Although the endpoint also provides limited
	// user profile functionality, the returned profile information is minimal.
	// Therefore, for richer profile data, Client uses the PeopleHub API (peopleHubEndpoint)
	// instead.
	//
	// Requests sent to this endpoint must include the 'X-Xbl-Contract-Version'
	// header set to '3'. The socialContractVersion request option can be also
	// used for this purpose.
	socialEndpoint = &url.URL{
		Scheme: "https",
		Host:   "social.xboxlive.com",
	}

	// socialContractVersion is an [internal.RequestOption] that sets the
	// 'X-Xbl-Contract-Version' header to '3' for requests made to the
	// socialEndpoint.
	socialContractVersion = internal.ContractVersion("3")
)
