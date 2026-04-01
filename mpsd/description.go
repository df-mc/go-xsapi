package mpsd

import (
	"encoding/json"
	"net/url"
	"slices"

	"github.com/google/uuid"
)

// endpoint is the base URL used for making request calls with the Multiplayer
// Session Directory (MPSD) for Xbox Live.
var endpoint = &url.URL{
	Scheme: "https",
	Host:   "sessiondirectory.xboxlive.com",
}

// SessionDescription describes a multiplayer session published in the session directory.
//
// A session description consists of immutable constants, mutable properties,
// and the set of members participating in the session.
type SessionDescription struct {
	// Constants contains immutable values associated with the multiplayer session.
	//
	// Fields in this struct cannot be modified after the session is published.
	Constants *SessionConstants `json:"constants,omitempty"`

	// RoleTypes contains role definitions associated with the session.
	//
	// The format and semantics of this field are currently undefined.
	RoleTypes json.RawMessage `json:"roleTypes,omitempty"`

	// Properties contains mutable properties associated with the multiplayer session.
	//
	// These properties may be updated at any time by the owner of the session.
	Properties *SessionProperties `json:"properties,omitempty"`

	// Members is a map whose keys are member identifiers (labels) and whose values
	// are the corresponding member descriptions.
	//
	// In addition to concrete numerical IDs for member, the reserved alias "me" may
	// be used as a key to refer to the member associated with the currently authenticated user.
	//
	// A member may modify only their own MemberDescription. Use of "me" is
	// equivalent to specifying the caller's own member ID.
	Members map[string]*MemberDescription `json:"members,omitempty"`
}

// SessionProperties contains mutable properties associated with a multiplayer session.
//
// Unlike SessionConstants, most fields in this struct may be updated over the
// lifetime of the session by the session owner.
type SessionProperties struct {
	// System contains system-defined properties for the session.
	System *SessionPropertiesSystem `json:"system,omitempty"`

	// Custom contains title-defined properties for the session.
	//
	// The format and semantics of this field are defined by the title. It is
	// commonly used to expose session metadata such as display names or
	// server details.
	Custom json.RawMessage `json:"custom,omitempty"`
}

// SessionPropertiesSystem defines system-specific, mutable properties for a
// multiplayer session.
type SessionPropertiesSystem struct {
	// Keywords is an optional list of keywords associated with the session.
	Keywords []string `json:"keywords,omitempty"`

	// Turn contains the member IDs that currently have the turn in a
	// turn-based session. The usage of this field is currently unknown.
	Turn []uint32 `json:"turn,omitempty"`

	// JoinRestriction specifies who may join an open session.
	//
	// This field has no effect on reservations and does not apply to
	// private or visible sessions.
	JoinRestriction string `json:"joinRestriction,omitempty"`

	// ReadRestriction specifies who may read an open session before joining.
	//
	// This field has no effect on reservations and does not apply to
	// private or visible sessions.
	ReadRestriction string `json:"readRestriction,omitempty"`

	// Closed indicates whether the session is joinable.
	//
	// When true, the session cannot be joined regardless of visibility,
	// join restrictions, or available capacity. This field does not
	// affect reservations. The default value is false.
	Closed bool `json:"closed"`

	// Locked indicates whether session membership is locked.
	//
	// When true, members who leave may rejoin the session, but no new
	// members may take their place. The default value is false.
	Locked bool `json:"locked,omitempty"`

	// Matchmaking is currently unknown, seems to be used for QoS measurement.
	Matchmaking json.RawMessage `json:"matchmaking,omitempty"`

	// MatchmakingResubmit indicates whether matchmaking should be resubmitted.
	//
	// When true, a previously found match is considered invalid and must be
	// resubmitted. When false, the match is considered successful and the
	// matchmaking service may release the session.
	MatchmakingResubmit bool `json:"matchmakingResubmit,omitempty"`

	// InitializationSucceeded indicates whether session initialization
	// completed successfully.
	InitializationSucceeded bool `json:"initializationSucceeded,omitempty"`

	// Host identifies the device token of the session host.
	Host string `json:"host,omitempty"`

	// ServerConnectionStringCandidates contains an ordered list of
	// case-insensitive connection strings that may be used to connect
	// to a game server.
	//
	// Titles typically use the first entry, but may apply custom selection
	// logic (for example, based on load).
	ServerConnectionStringCandidates json.RawMessage `json:"serverConnectionStringCandidates,omitempty"`
}

const (
	// SessionRestrictionNone indicates that the session is not visible to anyone.
	SessionRestrictionNone = "none"

	// SessionRestrictionLocal indicates that the session is visible only to
	// users on the same device.
	//
	// This restriction is rarely used by online multiplayer titles.
	SessionRestrictionLocal = "local"

	// SessionRestrictionFollowed indicates that the session is visible to
	// local users and users followed by an existing session member.
	SessionRestrictionFollowed = "followed"
)

// SessionConstants contains immutable constants associated with a multiplayer session.
//
// These values may be set by the session author or by the session template
// defined by the title. Fields in this struct cannot be modified after the
// session is published.
type SessionConstants struct {
	// System contains system-defined constants for the session.
	//
	// Fields defined by a session template cannot be overridden.
	System *SessionConstantsSystem `json:"system,omitempty"`

	// Custom contains title-defined constants for the session.
	Custom json.RawMessage `json:"custom,omitempty"`
}

// SessionConstantsSystem defines system-specific, immutable constants for a
// multiplayer session.
type SessionConstantsSystem struct {
	// MaxMembersCount is the maximum number of members allowed in the session.
	MaxMembersCount uint32 `json:"maxMembersCount,omitempty"`

	// Capabilities defines the capabilities supported by the session.
	//
	// The format and semantics of this field are currently undefined.
	Capabilities json.RawMessage `json:"capabilities,omitempty"`

	// Visibility specifies the visibility level of the session.
	Visibility string `json:"visibility,omitempty"`

	// Initiators contains the XUIDs of users who initiated the session.
	Initiators []string `json:"initiators,omitempty"`

	// ReservedRemovalTimeout is the maximum duration, in milliseconds, that a
	// reserved member may take to join the session before the reservation
	// is removed.
	ReservedRemovalTimeout uint64 `json:"reservedRemovalTimeout,omitempty"`

	// InactiveRemovalTimeout is the maximum duration, in milliseconds, that an
	// inactive member may remain inactive before being removed from the session.
	InactiveRemovalTimeout uint64 `json:"inactiveRemovalTimeout,omitempty"`

	// ReadyRemovalTimeout is the maximum duration, in milliseconds, that a
	// ready member may take to become active before transitioning to inactive.
	ReadyRemovalTimeout uint64 `json:"readyRemovalTimeout,omitempty"`

	// SessionEmptyTimeout is the maximum duration, in milliseconds, that a
	// session may remain empty before being deleted.
	SessionEmptyTimeout uint64 `json:"sessionEmptyTimeout,omitempty"`

	// Metrics is currently unknown, seems to be used for QoS measurement.
	Metrics json.RawMessage `json:"metrics,omitempty"`

	// MemberInitialization indicates that members are expected to perform
	// initialization after session creation.
	//
	// Initialization stages and timeouts are tracked automatically, including
	// initial Quality of Service (QoS) measurements when metrics are specified.
	MemberInitialization json.RawMessage `json:"memberInitialization,omitempty"`

	// PeerToPeerRequirements defines QoS requirements for peer-to-peer
	// connections between session members.
	PeerToPeerRequirements json.RawMessage `json:"peerToPeerRequirements,omitempty"`

	// PeerToHostRequirements defines QoS requirements for connections between
	// host candidates and session members.
	PeerToHostRequirements json.RawMessage `json:"peerToHostRequirements,omitempty"`

	// MeasurementServerAddresses contains the server endpoints to be evaluated
	// for QoS measurements.
	MeasurementServerAddresses json.RawMessage `json:"measurementServerAddresses,omitempty"`

	// CloudComputePackage contains Cloud Compute package constants for the session.
	CloudComputePackage json.RawMessage `json:"cloudComputePackage,omitempty"`
}

// MemberDescription describes a member participating in a multiplayer session.
//
// A member description consists of immutable constants, which are fixed once
// the member joins or publishes the session, and mutable properties, which may
// be updated over the lifetime of the session by the member themselves.
type MemberDescription struct {
	// Constants contains immutable, system-defined values
	// associated with the member.
	//
	// Fields in this struct cannot be modified after the member has joined or
	// published the multiplayer session.
	Constants *MemberConstants `json:"constants,omitempty"`

	// Properties contains mutable properties associated with the member.
	//
	// These properties may be updated at any time, but only by the member
	// to whom they belong.
	Properties *MemberProperties `json:"properties,omitempty"`
}

// MemberProperties contains mutable properties for a member in a multiplayer session.
type MemberProperties struct {
	// System contains system-defined properties for the member.
	System *MemberPropertiesSystem `json:"system,omitempty"`

	// Custom contains title-defined properties for the member.
	//
	// The format and semantics of this field are specific to the title.
	Custom json.RawMessage `json:"custom,omitempty"`
}

// MemberConstants contains immutable constants for a member in a multiplayer session.
type MemberConstants struct {
	// System contains system-defined constants for the member.
	System *MemberConstantsSystem `json:"system,omitempty"`

	// Custom contains title-defined constants for the member.
	//
	// The format and semantics of this field are specific to the title.
	Custom json.RawMessage `json:"custom,omitempty"`
}

// MemberConstantsSystem specifies the system-specific constants for a member in a multiplayer session.
type MemberConstantsSystem struct {
	// XUID is the user ID of the member.
	XUID string `json:"xuid,omitempty"`

	// Initialize indicates whether QoS initialization should be performed for
	// the member when joining the session.
	Initialize bool `json:"initialize,omitempty"`
}

// MemberPropertiesSystem defines system-specific, mutable properties for a member
// in a multiplayer session.
type MemberPropertiesSystem struct {
	// Active indicates whether the member is active in the multiplayer session.
	//
	// This value is typically true for most members. Joining or publishing a
	// multiplayer session requires this field to be set to true.
	Active bool `json:"active,omitempty"`

	// Ready indicates whether the member is ready for gameplay or matchmaking.
	//
	// This field is used by titles that support a ready-state concept prior
	// to matchmaking or session start.
	Ready bool `json:"ready,omitempty"`

	// Connection identifies the RTA subscription associated with this member.
	//
	// The value must match the ID specified in the corresponding
	// [rta.Subscription]. Once the member has joined or published the session,
	// notifications for the session will be delivered over this connection,
	// subject to the scope specified in [MemberPropertiesSystem.Subscription].
	Connection uuid.UUID `json:"connection"`

	// Subscription specifies which portions of the multiplayer session generate
	// notifications over the associated RTA subscription.
	Subscription *MemberPropertiesSystemSubscription `json:"subscription,omitempty"`

	// SecureDeviceAddress is a legacy identifier used by UWP and Xbox One titles.
	//
	// The value is base64-encoded and contains device and connection information
	// for session hosting. This field is not used by most modern games.
	SecureDeviceAddress []byte `json:"secureDeviceAddress,omitempty"`
}

// MemberPropertiesSystemSubscription specifies which portions in the multiplayer session
// that the member is currently in are subject to be notified in the connection between
// RTA subscriptions.
type MemberPropertiesSystemSubscription struct {
	// ID uniquely identifies the relationship between the RTA subscription
	// and the multiplayer session.
	//
	// The value must be an uppercase UUID (GUID) and is used exclusively
	// for this field. It must not be confused with ID, which
	// uses a standard (lowercase) UUID format.
	ID string `json:"id"`

	// ChangeTypes specifies the types of session changes that generate
	// notifications for the member over the associated RTA subscription.
	ChangeTypes []string `json:"changeTypes,omitempty"`
}

const (
	// ChangeTypeEverything indicates that all changes within the multiplayer session are
	// delivered to the member over the associated RTA subscription.
	ChangeTypeEverything = "everything"

	// ChangeTypeHost indicates that changes to the multiplayer session host
	// information are delivered to the member over the associated
	// RTA subscription.
	ChangeTypeHost = "host"

	// ChangeTypeInitialization indicates that changes to the session
	// initialization state are delivered to the member over the associated
	// RTA subscription.
	ChangeTypeInitialization = "initialization"

	// ChangeTypeMatchmakingStatus indicates that changes to the matchmaking
	// status of the multiplayer session (e.g. match found or match
	// expired) are delivered to the member over the associated
	// RTA subscription.
	ChangeTypeMatchmakingStatus = "matchmakingStatus"

	// ChangeTypeMembersList indicates that notifications are delivered when a
	// member joins the multiplayer session.
	//
	// This change type is used only to notify member join events.
	ChangeTypeMembersList = "membersList"

	// ChangeTypeMembersStatus indicates that notifications are delivered when a
	// member leaves the multiplayer session.
	//
	// This change type is used only to notify member leave events.
	ChangeTypeMembersStatus = "membersStatus"

	// ChangeTypeJoinability indicates that changes to the multiplayer session
	// joinability are delivered to the member over the associated RTA subscription.
	//
	// This includes changes to [SessionPropertiesSystem.ReadRestriction] and
	// [SessionPropertiesSystem.JoinRestriction].
	ChangeTypeJoinability = "joinability"

	// ChangeTypeCustomProperty indicates that changes to the custom properties
	// of the multiplayer session are delivered to the member over the associated
	// RTA subscription.
	ChangeTypeCustomProperty = "customProperty"

	// ChangeTypeMembersCustomProperty indicates that changes to the custom
	// properties of any session member are delivered to the member over the
	// associated RTA subscription.
	ChangeTypeMembersCustomProperty = "membersCustomProperty"
)

// cloneSessionConstants creates a deep copy of the given SessionConstants.
func cloneSessionConstants(in *SessionConstants) *SessionConstants {
	if in == nil {
		return nil
	}
	out := &SessionConstants{
		Custom: slices.Clone(in.Custom),
	}
	if in.System != nil {
		system := *in.System
		system.Capabilities = slices.Clone(in.System.Capabilities)
		system.Initiators = slices.Clone(in.System.Initiators)
		system.Metrics = slices.Clone(in.System.Metrics)
		system.MemberInitialization = slices.Clone(in.System.MemberInitialization)
		system.PeerToPeerRequirements = slices.Clone(in.System.PeerToPeerRequirements)
		system.PeerToHostRequirements = slices.Clone(in.System.PeerToHostRequirements)
		system.MeasurementServerAddresses = slices.Clone(in.System.MeasurementServerAddresses)
		system.CloudComputePackage = slices.Clone(in.System.CloudComputePackage)
		out.System = &system
	}
	return out
}

// cloneSessionProperties creates a deep copy of the given SessionProperties.
func cloneSessionProperties(in *SessionProperties) *SessionProperties {
	if in == nil {
		return nil
	}
	out := &SessionProperties{
		Custom: slices.Clone(in.Custom),
	}
	if in.System != nil {
		system := *in.System
		system.Keywords = slices.Clone(in.System.Keywords)
		system.Turn = slices.Clone(in.System.Turn)
		system.Matchmaking = slices.Clone(in.System.Matchmaking)
		system.ServerConnectionStringCandidates = slices.Clone(in.System.ServerConnectionStringCandidates)
		out.System = &system
	}
	return out
}

// cloneMemberDescription creates a deep copy of the given MemberDescription.
func cloneMemberDescription(in *MemberDescription) *MemberDescription {
	if in == nil {
		return nil
	}
	out := &MemberDescription{}
	if in.Constants != nil {
		out.Constants = &MemberConstants{
			Custom: slices.Clone(in.Constants.Custom),
		}
		if in.Constants.System != nil {
			system := *in.Constants.System
			out.Constants.System = &system
		}
	}
	if in.Properties != nil {
		out.Properties = &MemberProperties{
			Custom: slices.Clone(in.Properties.Custom),
		}
		if in.Properties.System != nil {
			system := *in.Properties.System
			system.SecureDeviceAddress = slices.Clone(in.Properties.System.SecureDeviceAddress)
			if in.Properties.System.Subscription != nil {
				subscription := *in.Properties.System.Subscription
				subscription.ChangeTypes = slices.Clone(in.Properties.System.Subscription.ChangeTypes)
				system.Subscription = &subscription
			}
			out.Properties.System = &system
		}
	}
	return out
}
