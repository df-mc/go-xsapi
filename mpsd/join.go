package mpsd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/google/uuid"
)

// JoinConfig describes a configuration for joining a multiplayer session in the directory.
// It can be passed to [Client.Join] to customize the join behavior.
type JoinConfig struct {
	// CustomMemberConstants holds immutable constants associated with the caller
	// as a member of the session. These are set when joining and cannot be
	// changed for the lifetime of the membership.
	//
	// The format and semantics of this field are defined by the title.
	CustomMemberConstants json.RawMessage

	// CustomMemberProperties holds mutable properties associated with the caller
	// as a member of the session. Unlike [JoinConfig.CustomMemberConstants],
	// these can be updated at any time during the session via
	// [Session.SetMemberCustomProperties].
	//
	// The format and semantics of this field are defined by the title.
	CustomMemberProperties json.RawMessage
}

// Join joins a multiplayer session in the directory using the provided handle ID.
//
// The handle ID is exposed on several handle types available in the directory, such as:
//   - [ActivityHandle] queried by [Client.Activities]
//   - [InviteHandle] sent by [Session.Invite] and received from notifications.
//
// The provided JoinConfig is applied to the caller's initial member contents.
// A zero-value JoinConfig may be sufficient for most titles.
//
// A Session may be returned, which represents the joined multiplayer session.
// Make sure to call [Session.Close] to leave the session when it is no longer needed.
func (c *Client) Join(ctx context.Context, handleID uuid.UUID, config JoinConfig, opts ...internal.RequestOption) (*Session, error) {
	_, payload, err := c.subscribe(ctx)
	if err != nil {
		return nil, err
	}

	d := SessionDescription{
		Members: map[string]*MemberDescription{
			"me": {
				Constants: &MemberConstants{
					System: &MemberConstantsSystem{
						Initialize: true,
						XUID:       c.userInfo.XUID,
					},
					Custom: config.CustomMemberConstants,
				},
				Properties: &MemberProperties{
					System: &MemberPropertiesSystem{
						Active:     true,
						Connection: payload.ConnectionID,
						Subscription: &MemberPropertiesSystemSubscription{
							ID:          strings.ToUpper(uuid.NewString()),
							ChangeTypes: []string{ChangeTypeEverything},
						},
					},
					Custom: config.CustomMemberProperties,
				},
			},
		},
	}

	// Join the multiplayer session by updating the members field to add the caller as participant.
	// This request call will fail if the multiplayer session does not exist.
	requestURL := endpoint.JoinPath("handles", handleID.String(), "session").String()
	req, err := internal.WithJSONBody(ctx, http.MethodPut, requestURL, d, append(opts,
		internal.RequestHeader("Content-Type", "application/json"),
		internal.RequestHeader("If-Match", "*"),
		internal.ContractVersion(contractVersion),
	))
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		loc := resp.Header.Get("Content-Location")
		if loc == "" {
			return nil, fmt.Errorf("Content-Location header is absent from response")
		}
		ref, err := parseSessionReference(loc)
		if err != nil {
			return nil, fmt.Errorf("parse session reference from Content-Location header: %w", err)
		}
		return c.createSession(ctx, ref, resp)
	default:
		return nil, internal.UnexpectedStatusCode(resp)
	}
}
