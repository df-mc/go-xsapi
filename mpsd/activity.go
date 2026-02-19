package mpsd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// Activities returns activity handles for open multiplayer sessions in the specified
// Service Configuration ID (SCID) for all users.
func (c *Client) Activities(ctx context.Context, scid uuid.UUID) ([]ActivityHandle, error) {
	return c.ActivitiesForUsers(ctx, scid, nil)
}

// ActivitiesForUsers returns activity handles for open multiplayer sessions
// associated with the specified XUIDs.
// The Service Configuration ID (SCID) identifies the game to query.
// If xuids is nil, it returns all open multiplayer sessions for the SCID.
func (c *Client) ActivitiesForUsers(ctx context.Context, scid uuid.UUID, xuids []string) ([]ActivityHandle, error) {
	// searchRequestPeople specifies whose perspective is used when searching
	// for activity handles to multiplayer sessions.
	type searchRequestPeople struct {
		// SocialGroup is the name of the social group used to filter users.
		// It can be "people" or "favorites".
		SocialGroup string `json:"moniker,omitempty"`
		// SocialGroupXUID is the XUID of the user to whom the social group applies.
		// In most cases, this is the caller's own XUID, since most games send search
		// requests from the player's own perspective.
		SocialGroupXUID string `json:"monikerXuid,omitempty"`
	}
	type searchRequestOwners struct {
		// XUIDs is a list that specifies user IDs to find all activities for.
		XUIDs  []string            `json:"xuids,omitempty"`
		People searchRequestPeople `json:"people,omitempty"`
	}
	// searchRequest represents the on-wire format used for searching
	// activity handles to open multiplayer sessions in the directory.
	type searchRequest struct {
		// Type indicates the type for the request.
		// For searchRequest, this is always "activity".
		Type string `json:"type"`
		// ServiceConfigID is the service configuration ID for this request.
		// A Service Configuration ID (SCID) may be shared by various titles
		// available on many platforms.
		ServiceConfigID uuid.UUID `json:"scid"`
		// Owner includes parameters used for querying activity handles
		// for multiplayer sessions in the directory.
		Owners searchRequestOwners `json:"owners"`
	}

	var (
		requestURL   = endpoint.JoinPath("handles/query")
		responseBody struct {
			Activities []ActivityHandle `json:"results"`
		}
	)
	requestURL.RawQuery = "include=relatedInfo,customProperties"
	return responseBody.Activities, c.do(ctx, http.MethodPost, requestURL.String(), searchRequest{
		Type:            "activity",
		ServiceConfigID: scid,
		Owners: searchRequestOwners{
			XUIDs: xuids,
			People: searchRequestPeople{
				SocialGroup:     "people",
				SocialGroupXUID: c.api.UserInfo().XUID,
			},
		},
	}, &responseBody)
}

// writeActivity publishes an activity handle for the multiplayer session.
//
// Publishing an activity handle makes the session visible to users who are
// searching for open multiplayer sessions in the session directory. The
// provided context controls request cancellation and deadlines.
func (s *Session) writeActivity(ctx context.Context) error {
	return s.client.do(ctx, http.MethodPost, endpoint.JoinPath("handles").String(), activityHandle{
		Type:             "activity",
		SessionReference: s.ref,
		Version:          1,
	}, nil)
}

// activityHandle is the wire representation used to create an activity handle
// for a multiplayer session.
//
// Activity handles are published to make sessions discoverable to users
// browsing or searching for open multiplayer sessions.
type activityHandle struct {
	// Type identifies the handle type.
	//
	// It is always "activity".
	Type string `json:"type"`

	// SessionReference identifies the multiplayer session associated with
	// the activity handle.
	SessionReference SessionReference `json:"sessionRef,omitempty"`

	// Version specifies the activity handle version.
	//
	// It is always 1.
	Version int `json:"version"`
}

// ActivityHandle represents a published activity handle for a multiplayer session.
//
// An activity handle is created by the session owner to advertise an open
// multiplayer session and make it discoverable in the session directory.
type ActivityHandle struct {
	activityHandle

	// CreateTime is the time at which the activity handle was created.
	CreateTime time.Time `json:"createTime,omitempty"`

	// CustomProperties contains title-defined metadata associated with the
	// multiplayer session.
	//
	// The format and semantics of this field are specific to the title.
	CustomProperties json.RawMessage `json:"customProperties,omitempty"`

	// GameTypes is currently unknown and unclear how it is used in retail games.
	GameTypes json.RawMessage `json:"gameTypes,omitempty"`

	// ID uniquely identifies the activity handle.
	ID uuid.UUID `json:"id,omitempty"`

	// InviteProtocol specifies the protocol used to invite users to the
	// multiplayer session.
	InviteProtocol string `json:"inviteProtocol,omitempty"`

	// RelatedInfo contains additional state and metadata associated with
	// the referenced multiplayer session.
	RelatedInfo *ActivityHandleRelatedInfo `json:"relatedInfo,omitempty"`

	// TitleID is the title ID associated with the multiplayer session.
	//
	// This value may differ from the title of the authenticated client,
	// as a single service configuration may be shared across multiple titles.
	TitleID string `json:"titleId,omitempty"`

	// OwnerXUID is the XUID of the user who created the multiplayer session.
	OwnerXUID string `json:"ownerXuid,omitempty"`
}

// URL returns the URL locating to the resource for the activity handle.
func (h ActivityHandle) URL() *url.URL {
	return endpoint.JoinPath(
		"handles",
		h.ID.String(),
	)
}

// ActivityHandleRelatedInfo contains additional metadata associated with
// a multiplayer activity handle.
type ActivityHandleRelatedInfo struct {
	// Closed indicates whether the multiplayer session is closed to new joins.
	Closed bool `json:"closed,omitempty"`

	// InviteProtocol specifies the protocol used when inviting the current
	// authenticated user.
	InviteProtocol string `json:"inviteProtocol,omitempty"`

	// JoinRestriction specifies the restriction applied when joining
	// the multiplayer session.
	JoinRestriction string `json:"joinRestriction,omitempty"`

	// MaxMembersCount is the maximum number of members allowed in the
	// multiplayer session.
	MaxMembersCount uint32 `json:"maxMembersCount,omitempty"`

	// PostedTime is the time at which the multiplayer session was created.
	PostedTime time.Time `json:"postedTime,omitempty"`

	// Visibility specifies the visibility of the multiplayer session.
	Visibility string `json:"visibility,omitempty"`
}
