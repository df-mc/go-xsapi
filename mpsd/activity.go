package mpsd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/google/uuid"
)

// Activities returns activity handles for open multiplayer sessions in the specified
// Service Configuration ID (SCID) for all users.
func (c *Client) Activities(ctx context.Context, scid uuid.UUID, opts ...internal.RequestOption) ([]ActivityHandle, error) {
	return c.ActivitiesForUsers(ctx, scid, nil, opts...)
}

// ActivitiesForUsers returns activity handles for open multiplayer sessions
// associated with the specified XUIDs.
// The Service Configuration ID (SCID) identifies the game to query.
// If xuids is nil, it returns all open multiplayer sessions for the SCID.
func (c *Client) ActivitiesForUsers(ctx context.Context, scid uuid.UUID, xuids []string, opts ...internal.RequestOption) ([]ActivityHandle, error) {
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
		requestURL = endpoint.JoinPath("handles/query")
		result     struct {
			Activities []ActivityHandle `json:"results"`
		}
	)
	requestURL.RawQuery = "include=relatedInfo,customProperties"
	if err := internal.Do(ctx, c.client, http.MethodPost, requestURL.String(), searchRequest{
		Type:            "activity",
		ServiceConfigID: scid,
		Owners: searchRequestOwners{
			XUIDs: xuids,
			People: searchRequestPeople{
				SocialGroup:     "people",
				SocialGroupXUID: c.userInfo.XUID,
			},
		},
	}, &result, append(opts,
		internal.RequestHeader("Content-Type", "application/json"),
		internal.ContractVersion(contractVersion),
	)); err != nil {
		return nil, err
	}
	return result.Activities, nil
}

// Invite invites the user identified by the XUID to the multiplayer session.
// If successful, it returns an InviteHandle describing the created invitation.
// The invite handle includes service-defined metadata such as the expiration time.
// The title ID is specific to the game
func (s *Session) Invite(ctx context.Context, xuid, titleID string, opts ...internal.RequestOption) (*InviteHandle, error) {
	var handle *InviteHandle
	if err := internal.Do(ctx, s.client.client, http.MethodPost, endpoint.JoinPath("handles").String(), inviteHandle{
		Type:             "invite",
		SessionReference: s.ref,
		Version:          1,
		InvitedXUID:      xuid,
		InviteAttributes: InviteAttributes{
			TitleID: titleID,
		},
	}, &handle, append(opts,
		internal.RequestHeader("Content-Type", "application/json"),
		internal.ContractVersion(contractVersion),
	)); err != nil {
		return nil, err
	}
	if handle == nil {
		return nil, errors.New("mpsd: invalid invite response")
	}
	return handle, nil
}

// inviteHandle is the wire representation used to invite a user to the
// multiplayer session.
type inviteHandle struct {
	// Type identifies the handle type.
	//
	// It is always "invite".
	Type string `json:"type"`

	// SessionReference contains a reference to the multiplayer session
	// to invite the specified XUID.
	SessionReference SessionReference `json:"sessionRef"`

	// Version is the version of the invite handle.
	//
	// It is always 1.
	Version int `json:"version"`

	// InvitedXUID is the XUID of the user to be invited to
	// the multiplayer session referenced by this invite handle.
	InvitedXUID string `json:"invitedXuid"`

	// InviteAttributes contains the attributes associated with the invite handle.
	InviteAttributes InviteAttributes `json:"inviteAttributes"`
}

// InviteHandle represents a published handle for an invite to a multiplayer session.
//
// An invitation handle is sent by the session participant to the user referenced by XUID.
type InviteHandle struct {
	inviteHandle

	// ID is the unique ID associated with the invite handle.
	ID uuid.UUID `json:"id"`

	// GameTypes is a map whose keys are platform name such as "uwp-desktop" or
	// "android", and whose values are structs that describes a single title
	// associated with the invite handle.
	GameTypes map[string]GameType `json:"gameTypes"`

	// SenderXUID is the XUID of the user that has sent the invite.
	SenderXUID string `json:"senderXuid"`

	// Expiration indicates the time that this invite handle will expire.
	Expiration time.Time `json:"expiration"`

	// InviteProtocol is the protocol used for invitation.
	// It is unknown how it is used. It is always "game".
	// Supported values might also include "party".
	InviteProtocol string `json:"inviteProtocol"`
}

// GameType describes a single title available for a specific platform.
type GameType struct {
	// TitleID is the title ID associated with the game type.
	TitleID string `json:"titleId"`

	// PackageFamilyName is the package family name associated with the game type.
	PackageFamilyName string `json:"pfn"`

	// BoundPackageFamilyNames lists other package family names associated with the game type.
	BoundPackageFamilyNames []string `json:"boundPfns"`
}

// InviteAttributes describes attributes associated with the InviteHandle.
type InviteAttributes struct {
	// TitleID is the title ID associated with the invite handle.
	// For invitation requests, it must be the title authenticated
	// by the XSTS token.
	TitleID string `json:"titleId"`

	// ContextStringID is the optional ID used to activate the
	// invite handle. It is unknown how it is used or how it is
	// generated during invitation.
	ContextStringID string `json:"contextString,omitempty"`

	// Context is the optional context associated with the invite
	// handle, encapsulated in a string. The format and semantics
	// are specific to the title.
	Context string `json:"context,omitempty"`
}

// writeActivity publishes an activity handle for the multiplayer session.
//
// Publishing an activity handle makes the session visible to users who are
// searching for open multiplayer sessions in the session directory. The
// provided context controls request cancellation and deadlines.
func (s *Session) writeActivity(ctx context.Context) error {
	return internal.Do(ctx, s.client.client, http.MethodPost, endpoint.JoinPath("handles").String(), activityHandle{
		Type:             "activity",
		SessionReference: s.ref,
		Version:          1,
	}, nil, []internal.RequestOption{
		internal.RequestHeader("Content-Type", "application/json"),
		internal.ContractVersion(contractVersion),
	})
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
	SessionReference SessionReference `json:"sessionRef"`

	// Version is the version of the activity handle.
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
	CreateTime time.Time `json:"createTime"`

	// CustomProperties contains title-defined metadata associated with the
	// multiplayer session.
	//
	// The format and semantics of this field are specific to the title.
	CustomProperties json.RawMessage `json:"customProperties"`

	// GameTypes is currently unknown and unclear how it is used in retail games.
	GameTypes json.RawMessage `json:"gameTypes"`

	// ID uniquely identifies the activity handle.
	ID uuid.UUID `json:"id"`

	// InviteProtocol specifies the protocol used to invite users to the
	// multiplayer session.
	InviteProtocol string `json:"inviteProtocol"`

	// RelatedInfo contains additional state and metadata associated with
	// the referenced multiplayer session.
	RelatedInfo *ActivityHandleRelatedInfo `json:"relatedInfo"`

	// TitleID is the title ID associated with the multiplayer session.
	//
	// This value may differ from the title of the authenticated client,
	// as a single service configuration may be shared across multiple titles.
	TitleID string `json:"titleId"`

	// OwnerXUID is the XUID of the user who created the multiplayer session.
	OwnerXUID string `json:"ownerXuid"`
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
	Closed bool `json:"closed"`

	// InviteProtocol specifies the protocol used when inviting the current
	// authenticated user.
	InviteProtocol string `json:"inviteProtocol"`

	// JoinRestriction specifies the restriction applied when joining
	// the multiplayer session.
	JoinRestriction string `json:"joinRestriction"`

	// MaxMembersCount is the maximum number of members allowed in the
	// multiplayer session.
	MaxMembersCount uint32 `json:"maxMembersCount"`

	// PostedTime is the time at which the multiplayer session was created.
	PostedTime time.Time `json:"postedTime"`

	// Visibility specifies the visibility of the multiplayer session.
	Visibility string `json:"visibility"`
}
