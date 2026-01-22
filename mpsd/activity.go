package mpsd

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
)

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
