package mpsd

import (
	"context"
	"encoding/json"
	"github.com/df-mc/go-xsapi/rta"
	"sync/atomic"
)

type Session struct {
	ref  SessionReference
	conf PublishConfig

	rta *rta.Conn

	sub *rta.Subscription

	h atomic.Pointer[Handler]
}

func (s *Session) Query() (*Commit, error) {
	q := Query{Client: s.conf.Client}
	return q.Query(nil, s.ref)
}

func (s *Session) Close() error {
	if err := s.rta.Unsubscribe(context.Background(), s.sub); err != nil {
		s.conf.Logger.Error("error unsubscribing with RTA", "err", err)
	}
	_, err := s.Commit(context.Background(), &SessionDescription{
		Members: map[string]*MemberDescription{
			"me": nil,
		},
	})
	return err
}

type SessionDescription struct {
	Constants  *SessionConstants             `json:"constants,omitempty"`
	RoleTypes  json.RawMessage               `json:"roleTypes,omitempty"`
	Properties *SessionProperties            `json:"properties,omitempty"`
	Members    map[string]*MemberDescription `json:"members,omitempty"`
}

// SessionProperties is a set of properties associated with multiplayer session.
// Any member can modify these fields.
type SessionProperties struct {
	System *SessionPropertiesSystem `json:"system,omitempty"`
	// Custom is a JSON string that specify the custom properties for the session. These can
	// be changed anytime.
	Custom json.RawMessage `json:"custom,omitempty"`
}

type SessionPropertiesSystem struct {
	// Keywords is an optional list of keywords associated with the session.
	Keywords []string `json:"keywords,omitempty"`
	// Turn is a list of member IDs indicating whose turn it is.
	Turn []uint32 `json:"turn,omitempty"`
	// JoinRestriction restricts who can join "open" sessions. (Has no effects on reservations,
	// which means it has no impact on "private" and "visible" sessions)
	// It is one of constants defined below.
	JoinRestriction string `json:"joinRestriction,omitempty"`
	// ReadRestriction restricts who can read "open" sessions. (Has no effect on reservations,
	// which means it has no impact on "private" and "visible" sessions.)
	ReadRestriction string `json:"readRestriction,omitempty"`
	// Controls whether a session is joinable, independent of visibility, join restriction,
	// and available space in the session. Does not affect reservations. Defaults to false.
	Closed bool `json:"closed"`
	// If Locked is true, it would allow the members of the session to be locked, such that
	// if a user leaves they are able to come back into the session but no other user could
	// take that spot. Defaults to false.
	Locked      bool                                `json:"locked,omitempty"`
	Matchmaking *SessionPropertiesSystemMatchmaking `json:"matchmaking,omitempty"`
	// MatchmakingResubmit is true, if the match that was found didn't work out and needs to
	// be resubmitted. If false, signal that the match did work, and the matchmaking service
	// can release the session.
	MatchmakingResubmit bool `json:"matchmakingResubmit,omitempty"`
	// InitializationSucceeded is true if initialization succeeded.
	InitializationSucceeded bool `json:"initializationSucceeded,omitempty"`
	// Host is the device token of the host.
	Host string `json:"host,omitempty"`
	// ServerConnectionStringCandidates is the ordered list of case-insensitive connection
	// strings that the session could use to connect to a game server. Generally titles
	// should use the first on the list, but sophisticated titles could use a custom mechanism
	// for choosing one of the others (e.g. based on load).
	ServerConnectionStringCandidates json.RawMessage `json:"serverConnectionStringCandidates,omitempty"`
}

type SessionPropertiesSystemMatchmaking struct {
	// TargetSessionConstants is a JSON string representing the target session constants.
	TargetSessionConstants json.RawMessage `json:"targetSessionConstants,omitempty"`
	// ServerConnectionString Force a specific connection string to be used. This is useful
	// for session in progress join scenarios.
	ServerConnectionString string `json:"serverConnectionString,omitempty"`
}

const (
	SessionRestrictionNone     = "none"
	SessionRestrictionLocal    = "local"
	SessionRestrictionFollowed = "followed"
)

// SessionConstants represents constants for a multiplayer session.
//
// SessionConstants are set by the creator or by the session template only when a
// session is created. Fields in SessionConstants generally cannot be changed after
// the session is created.
type SessionConstants struct {
	System *SessionConstantsSystem `json:"system,omitempty"`
	// Custom is any custom constants for the session, specified in a JSON string.
	Custom json.RawMessage `json:"custom,omitempty"`
}

type SessionConstantsSystem struct {
	// MaxMembersCount is the maximum number of members in the session.
	MaxMembersCount uint32 `json:"maxMembersCount,omitempty"`
	// Capabilities is the capabilities of the session.
	Capabilities *SessionCapabilities `json:"capabilities,omitempty"`
	// Visibility is the visibility of the session.
	Visibility string `json:"visibility,omitempty"`
	// Initiators is a list of XUIDs indicating who initiated the session.
	Initiators []string `json:"initiators,omitempty"`
	// ReservedRemovalTimeout is the maximum time, in milliseconds, for a member with a reservation
	// to join the session. If the member doesn't join within this time, this reservation is removed.
	ReservedRemovalTimeout uint64 `json:"reservedRemovalTimeout,omitempty"`
	// InactiveRemovalTimeout is the maximum time, in milliseconds, for an inactive member to become
	// active. If an inactive member doesn't become active within this time, the member is removed from
	// the session.
	InactiveRemovalTimeout uint64 `json:"inactiveRemovalTimeout,omitempty"`
	// ReadyRemovalTimeout is the maximum time, in milliseconds, for a member who is marked as ready
	// to become active. When the shell launches the title to start a multiplayer game, the member is
	// marked as ready. If a member who is marked as ready doesn't become active with in this time,
	// the member becomes inactive.
	ReadyRemovalTimeout uint64 `json:"readyRemovalTimeout,omitempty"`
	// SessionEmptyTimeout is the maximum time, in milliseconds, that the session can remain empty.
	// If no members join the session within this time, the session is deleted.
	SessionEmptyTimeout uint64                         `json:"sessionEmptyTimeout,omitempty"`
	Metrics             *SessionConstantsSystemMetrics `json:"metrics,omitempty"`
	// If MemberInitialization is set, the session expects the client system or title to perform initialization
	// after session creation. Timeouts and initialization stages are automatically tracked by the session, including
	// initial Quality of Service (QoS) measurements if any metrics are set.
	MemberInitialization *MemberInitialization `json:"memberInitialization,omitempty"`
	// PeerToPeerRequirements is a QoS requirements for a connection between session members.
	PeerToPeerRequirements *PeerToPeerRequirements `json:"peerToPeerRequirements,omitempty"`
	// PeerToHostRequirements is a QoS requirements for a connection between a host candidate
	// and session members.
	PeerToHostRequirements *PeerToHostRequirements `json:"peerToHostRequirements,omitempty"`
	// MeasurementServerAddresses is the set of potential server connection strings that should
	// be evaluated.
	MeasurementServerAddresses json.RawMessage `json:"measurementServerAddresses,omitempty"`
	// CloudComputePackage is the Cloud Compute package constants for the session, specified in a JSON string.
	CloudComputePackage json.RawMessage `json:"cloudComputePackage,omitempty"`
}

type PeerToHostRequirements struct {
	LatencyMaximum       uint64 `json:"latencyMaximum,omitempty"`
	BandwidthDownMinimum uint64 `json:"bandwidthDownMinimum,omitempty"`
	BandwidthUpMinimum   uint64 `json:"bandwidthUpMinimum,omitempty"`
	HostSelectionMetric  string `json:"hostSelectionMetric,omitempty"`
}

const (
	HostSelectionMetricBandwidthUp   = "bandwidthUp"
	HostSelectionMetricBandwidthDown = "bandwidthDown"
	HostSelectionMetricBandwidth     = "bandwidth"
	HostSelectionMetricLatency       = "latency"
)

type PeerToPeerRequirements struct {
	LatencyMaximum   uint64 `json:"latencyMaximum,omitempty"`
	BandwidthMinimum uint64 `json:"bandwidthMinimum,omitempty"`
}

type MemberInitialization struct {
	JoinTimeout          uint64 `json:"joinTimeout,omitempty"`
	MeasurementTimeout   uint64 `json:"measurementTimeout,omitempty"`
	EvaluationTimeout    uint64 `json:"evaluationTimeout,omitempty"`
	ExternalEvaluation   bool   `json:"externalEvaluation,omitempty"`
	MembersNeededToStart uint32 `json:"membersNeededToStart,omitempty"`
}

type SessionConstantsSystemMetrics struct {
	// Latency indicates that the title wants latency measured to
	// help determine connectivity.
	Latency bool `json:"latency,omitempty"`
	// Bandwidth indicates that the title wants downstream (host-to-peer)
	// bandwidth measured to help determine connectivity.
	BandwidthDown bool `json:"bandwidthDown,omitempty"`
	// BandwidthUp indicates that the title wants upstream (peer-to-host)
	// bandwidth measured to help determine connectivity.
	BandwidthUp bool `json:"bandwidthUp,omitempty"`
	// Custom indicates that the title wants a custom measurement to help
	// determine connectivity.
	Custom bool `json:"custom,omitempty"`
}

// SessionCapabilities represents the capabilities of multiplayer session.
//
// SessionCapabilities are optional bool values that are set in the session
// template. If no capabilities are needed, an empty SessionCapabilities should
// be used in the template to prevent capabilities from being specified at session
// creation, unless the title requires dynamic session capabilities.
type SessionCapabilities struct {
	// Connectivity indicates whether a session can enable metrics.
	Connectivity bool `json:"connectivity,omitempty"`
	// If SuppressPresenceActivityCheck is false (the default value), active users are required to
	// remain online playing the title. If they don't, they are demoted to inactive status. Set
	// SuppressPresenceActivityCheck to true to enable session members to stay active indefinitely
	SuppressPresenceActivityCheck bool `json:"suppressPresenceActivityCheck,omitempty"`
	// Gameplay indicates whether the session represents actual gameplay rather than time in setup
	// or a menu, such as a lobby or during matchmaking.
	Gameplay bool `json:"gameplay,omitempty"`
	// If Large is true, if the session can host 101 to 1000 users, which affects other session features.
	// Otherwise, the session can host 1 to 100 users.
	Large bool `json:"large,omitempty"`
	// If UserAuthorizationStyle is true, the session supports calls from platforms without strong
	// title identity. This capability can't be set on large sessions.
	UserAuthorizationStyle bool `json:"userAuthorizationStyle,omitempty"`
	// If ConnectionRequiredForActiveMembers is true, a connection is required for a member to be
	// marked as active. To enable session notifications and detect disconnections, it must be set
	// to true.
	ConnectionRequiredForActiveMembers bool `json:"connectionRequiredForActiveMembers,omitempty"`
	// CrossPlay is true, if the session supports crossplay.
	CrossPlay bool `json:"crossPlay,omitempty"`
	// If Searchable is true, the session can be linked to a search handle for searching.
	Searchable bool `json:"searchable,omitempty"`
	// If HasOwners is true, the session has owners.
	HasOwners bool `json:"hasOwners,omitempty"`
}

const (
	SessionVisibilityPrivate = "private"
	SessionVisibilityVisible = "visible"
	SessionVisibilityOpen    = "open"
)
