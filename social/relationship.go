package social

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/df-mc/go-xsapi/v2/internal"
)

// Follow establishes a one-way follow relationship with the user identified
// by XUID.
//
// A follow relationship is applied immediately and does not require approval
// from the target user. Upon success, the target user's follower count is
// updated accordingly.
func (c *Client) Follow(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	requestURL := socialEndpoint.JoinPath(
		"/users/me/people/xuid(" + xuid + ")",
	).String()

	// Unlike [Client.AddFriend], this request call returns 204 No Content.
	return c.doRelationship(ctx, http.MethodPut, requestURL, opts, http.StatusOK, http.StatusNoContent)
}

// Unfollow removes an existing follow relationship with the user identified by XUID.
// If no follow relationship exists, an error will be returned. Therefore, it is recommended
// to check if the caller has an existing follow relationship with [Client.UserByXUID].
func (c *Client) Unfollow(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	return c.deleteRelationship(ctx, xuid, "follows", opts)
}

// RemoveMutualFollow removes the caller's follow of the user identified by
// XUID using the legacy people endpoint. Xbox Live keeps the user's follow of
// the caller, so to end a friendship in both directions use [Client.RemoveFriend]
// and [Client.RemoveFollower] instead.
func (c *Client) RemoveMutualFollow(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	requestURL := socialEndpoint.JoinPath(
		"/users/me/people/xuid(" + xuid + ")",
	).String()

	return c.doRelationship(ctx, http.MethodDelete, requestURL, opts, http.StatusOK, http.StatusNoContent)
}

// RemoveFollower removes the follow relationship the user identified by XUID
// has towards the caller, so the user no longer follows the caller. It is
// primarily useful for dropping followers whose privacy or enforcement
// restrictions prevent a friendship from being established.
func (c *Client) RemoveFollower(ctx context.Context, xuid string, opts ...internal.RequestOption) error {
	requestURL := socialEndpoint.JoinPath(
		"/users/me/people/follower/xuid(" + xuid + ")",
	).String()
	return c.doRelationship(ctx, http.MethodDelete, requestURL, opts, http.StatusOK, http.StatusNoContent)
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

	return c.doRelationship(ctx, http.MethodPut, requestURL, opts, http.StatusOK, http.StatusCreated)
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
	return c.deleteRelationship(ctx, xuid, "friends", opts)
}

// deleteRelationship removes a specific type of relationship with the user
// identified by XUID. The relationships can be "friends" or "follows".
func (c *Client) deleteRelationship(ctx context.Context, xuid, relationship string, opts []internal.RequestOption) error {
	requestURL := socialEndpoint.JoinPath(
		"/users/me/people/friends/v2",
		"xuid("+xuid+")",
	)
	q := requestURL.Query()
	q.Set("deleteRelationships", relationship)
	requestURL.RawQuery = q.Encode()

	return c.doRelationship(ctx, http.MethodDelete, requestURL.String(), opts, http.StatusOK, http.StatusCreated)
}

// AddFriends creates or accepts friend relationships with all users
// identified by the given XUIDs in a single request, and reports which users
// Xbox Live updated. It behaves like [Client.AddFriend] for each user, but a
// single bulk call avoids per-user rate limits when accepting many pending
// requests at once.
func (c *Client) AddFriends(ctx context.Context, xuids []string, opts ...internal.RequestOption) (BulkFriendsResult, error) {
	requestURL := socialEndpoint.JoinPath(
		"/bulk/users/me/people/friends/v2",
	)
	q := requestURL.Query()
	q.Set("method", "add")
	requestURL.RawQuery = q.Encode()

	req, err := internal.WithJSONBody(ctx, http.MethodPost, requestURL.String(), bulkFriendsRequest{XUIDs: xuids}, append(
		opts,
		socialContractVersion,
		internal.RequestHeader("Accept", "application/json"),
		internal.RequestHeader("Content-Type", "application/json"),
		internal.RequestHeader("Cache-Control", "no-cache"),
		internal.DefaultLanguage,
	))
	if err != nil {
		return BulkFriendsResult{}, fmt.Errorf("make request: %w", err)
	}
	return c.doBulkFriends(req, http.StatusOK, http.StatusCreated)
}

// RemoveFriends removes or denies friend relationships with all users identified
// by XUIDs, and reports which users Xbox Live updated.
func (c *Client) RemoveFriends(ctx context.Context, xuids []string, opts ...internal.RequestOption) (BulkFriendsResult, error) {
	requestURL := socialEndpoint.JoinPath(
		"/bulk/users/me/people/friends/v2",
	)
	q := requestURL.Query()
	q.Set("method", "remove")
	q.Set("deleteRelationships", "friends")
	requestURL.RawQuery = q.Encode()

	req, err := internal.WithJSONBody(ctx, http.MethodPost, requestURL.String(), bulkFriendsRequest{XUIDs: xuids}, append(
		opts,
		socialContractVersion,
		internal.RequestHeader("Accept", "application/json"),
		internal.RequestHeader("Content-Type", "application/json"),
		internal.RequestHeader("Cache-Control", "no-cache"),
		internal.DefaultLanguage,
	))
	if err != nil {
		return BulkFriendsResult{}, fmt.Errorf("make request: %w", err)
	}
	return c.doBulkFriends(req, http.StatusOK)
}

type (
	// bulkFriendsRequest is the wire representation of a bulk friend mutation
	// request body.
	bulkFriendsRequest struct {
		// XUIDs lists the XUIDs of the users whose friend relationships are mutated.
		XUIDs []string `json:"xuids"`
	}

	// BulkFriendsResult is the per-user outcome of [Client.AddFriends] or
	// [Client.RemoveFriends]. A successful response may still list failed users.
	BulkFriendsResult struct {
		// Updated lists the XUIDs whose relationships were updated by the request.
		Updated []string `json:"updatedPeople"`
		// Failed lists the XUIDs whose relationships couldn't be updated by the request.
		Failed []string `json:"failedToUpdate"`
	}
)

// doBulkFriends sends a bulk friend mutation and decodes its per-user result.
func (c *Client) doBulkFriends(req *http.Request, successCodes ...int) (BulkFriendsResult, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return BulkFriendsResult{}, err
	}
	defer resp.Body.Close()
	if !slices.Contains(successCodes, resp.StatusCode) {
		return BulkFriendsResult{}, responseError(resp)
	}
	var result BulkFriendsResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return BulkFriendsResult{}, fmt.Errorf("decode response body: %w", err)
	}
	return result, nil
}

// doRelationship sends a relationship mutation request and converts non-success
// responses into ResponseError values.
func (c *Client) doRelationship(ctx context.Context, method, requestURL string, opts []internal.RequestOption, successCodes ...int) error {
	req, err := internal.NewRequest(ctx, method, requestURL, nil, append(
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
	for _, code := range successCodes {
		if resp.StatusCode == code {
			return nil
		}
	}
	return responseError(resp)
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
