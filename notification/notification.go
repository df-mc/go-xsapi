package notification

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"
)

// Notification represents a notification received from Xbox Live.
//
// The following types implement this interface:
// - [GameInvite]
// - [Followers]
// - [IncomingFriendRequests]
// - [AcceptedFriendRequests]
type Notification interface {
	// SubscriptionCategory returns the subscription category of the notification/action.
	// It is one of the constants below.
	SubscriptionCategory() string
	// SubscriptionType returns the subscription type of the notification/action.
	// It is one of the constants below.
	SubscriptionType() string
	// SubscriptionID returns the subscription ID of the notification/action.
	SubscriptionID() string
}

const (
	SubscriptionCategoryMultiplayer = "Microsoft.Xbox.Multiplayer"
	SubscriptionCategoryPeople      = "Microsoft.Xbox.People"
)

const (
	SubscriptionTypeGameInvite = "GameInvites"

	SubscriptionTypeFollowers              = "Followers"
	SubscriptionTypeAcceptedFriendRequests = "AcceptedFriendRequests"
	SubscriptionTypeIncomingFriendRequests = "IncomingFriendRequests"
)

// Unmarshal decodes the given JSON data into a Notification.
func Unmarshal(b []byte) (Notification, error) {
	var k struct {
		notificationKey
		ActionID string `json:"ActionId"`
	}
	if err := json.Unmarshal(b, &k); err != nil {
		return nil, err
	}
	if k.ActionID != "" {
		return nil, errors.New("notification: cannot decode action")
	}
	if k.SubscriptionCategory == "" || k.SubscriptionType == "" {
		return nil, errors.New("notification: SubscriptionCategory or SubscriptionType is empty")
	}
	f, ok := defaultPool[notificationKey{
		SubscriptionCategory: k.SubscriptionCategory,
		SubscriptionType:     k.SubscriptionType,
	}]
	if !ok {
		return &Unknown{
			Category: k.SubscriptionCategory,
			Type:     k.SubscriptionType,
			ID:       k.SubscriptionID,
			Raw:      b,
		}, nil
	}
	n := f()
	if err := json.Unmarshal(b, n); err != nil {
		return nil, err
	}
	return n, nil
}

// Unknown is returned by [Unmarshal] when the given notification are not supported by this package.
// Callers can decode the raw JSON data contained in [Unknown.Raw] into their own representation.
type Unknown struct {
	// Category is the subscription category of the notification.
	Category string `json:"SubscriptionCategory"`
	// Type is the subscription type of the notification.
	Type string `json:"SubscriptionType"`
	// ID is the subscription ID of the notification.
	ID string `json:"SubscriptionId"`

	// Raw is the raw JSON data that were passed to [Unmarshal].
	Raw json.RawMessage `json:"-"`
}

// SubscriptionCategory implements [Notification.SubscriptionCategory].
func (u *Unknown) SubscriptionCategory() string {
	return u.Category
}

// SubscriptionType implements [Notification.SubscriptionType].
func (u *Unknown) SubscriptionType() string {
	return u.Type
}

// SubscriptionID implements [Notification.SubscriptionID].
func (u *Unknown) SubscriptionID() string {
	return u.ID
}

// init registers all notifications supported by this package.
func init() {
	register(SubscriptionCategoryMultiplayer, SubscriptionTypeGameInvite, func() Notification { return &GameInvite{} })

	register(SubscriptionCategoryPeople, SubscriptionTypeFollowers, func() Notification { return &Followers{} })
	register(SubscriptionCategoryPeople, SubscriptionTypeAcceptedFriendRequests, func() Notification { return &AcceptedFriendRequests{} })
	register(SubscriptionCategoryPeople, SubscriptionTypeIncomingFriendRequests, func() Notification { return &IncomingFriendRequests{} })
}

// pool maps a notificationKey to a function that generates a Notification
// that can be decoded with the JSON data received from the service.
type pool map[notificationKey]func() Notification

// categories returns all the categories registered in the pool.
// It is used as the default value for the filter.
func (p pool) categories() []string {
	category := make(map[string]struct{})
	for k := range p {
		category[k.SubscriptionCategory] = struct{}{}
	}
	return slices.Collect(maps.Keys(category))
}

// types returns all the types registered in the pool.
// It is used as the default value for the filter.
func (p pool) types() []string {
	types := make(map[string]struct{})
	for k := range p {
		types[k.SubscriptionType] = struct{}{}
	}
	return slices.Collect(maps.Keys(types))
}

// defaultPool is the pool that is used by register.
var defaultPool = make(pool)

// register registers the function that generates a Notification that
// can be decoded in JSON with the subscription category and the type.
func register(category, typ string, f func() Notification) {
	defaultPool[notificationKey{
		SubscriptionCategory: category,
		SubscriptionType:     typ,
	}] = f
}

// notificationKey is a struct containing the minimal set of fields present
// on every notification.
type notificationKey struct {
	// SubscriptionCategory is the subscription category of the notification.
	SubscriptionCategory string
	// SubscriptionType is the subscription type of the notification.
	SubscriptionType string
	// SubscriptionID is the subscription ID of the notification.
	SubscriptionID string `json:"SubscriptionId"`
}

// notification is the struct that contains all fields that are essential to represent
// a notification. All types that implement Notification must embed this struct.
type notification[A any] struct {
	// Actions lists all actions available to interact with this notification.
	// If some actions don't fit into Actions, OtherActionCount becomes non-zero.
	Actions []A
	// OtherActionCount counts other actions that weren't included in Actions.
	// This field may become non-zero when [InboxFilter.MaxActions] is less than
	// the number of actions contained in this notification.
	OtherActionCount int
	// MarkedRead indicates whether the caller has marked this notification as read.
	// Callers can mark this notification as read using [Client.MarkRead].
	// Even after a notification has already been marked as read, additional actions
	// may be appended to Actions in the future. In that case, this field may be marked
	// as false again that time.
	MarkedRead bool
	// Seen indicates whether the caller has seen this notification.
	// Callers can mark this notification as seen using [Client.MarkSeen].
	// It is unknown how it is different from MarkedRead.
	Seen bool
	// HasToasted indicates whether this notification has toasted to the caller.
	HasToasted bool
	// Image is the URL used as the thumbnail for the notification.
	Image string
	// ImageType determines how the Image field should be interpreted.
	// It is one of the constants below.
	ImageType int
	// Category is the subscription category of the notification.
	Category string `json:"SubscriptionCategory"`
	// Type is the subscription type of the notification.
	Type string `json:"SubscriptionType"`
	// ID is the subscription ID of the notification.
	ID string `json:"SubscriptionId"`
}

// SubscriptionID implements [Notification.SubscriptionID].
func (n *notification[A]) SubscriptionID() string {
	return n.ID
}

// SubscriptionCategory implements [Notification.SubscriptionCategory].
func (n *notification[A]) SubscriptionCategory() string {
	return n.Category
}

// SubscriptionType implements [Notification.SubscriptionType].
func (n *notification[A]) SubscriptionType() string {
	return n.Type
}

// UnmarshalJSON decodes the given JSON data into n with patches
// to support decoding payload received from WebSocket service.
func (n *notification[A]) UnmarshalJSON(b []byte) error {
	type Alias notification[A]
	data := struct {
		*Alias

		// Action is a single action that may be contained in a
		// notification received via WebSocket service.
		Action *A `json:"Action"`
	}{Alias: (*Alias)(n)}
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	if data.Action != nil {
		data.Actions = append(data.Actions, *data.Action)
	}

	// Please do not remove this check. There may be notifications without actions, but the API's
	// format is inconsistent enough that this check helps us notice when something has changed.
	if len(data.Actions) == 0 {
		return errors.New("notification: attempted to decode notifications without actions")
	}
	return nil
}

// Location represents the location at which an action would be invoked.
type Location struct {
	// PackageFamilyName is the package family name of the title.
	// It is only populated when [Location.Type] is [LocationTypeTitle].
	PackageFamilyName string `json:"Pfn"`
	// DisplayName is the display name of the title.
	// It is only populated when [Location.Type] is [LocationTypeTitle].
	DisplayName string

	// Name is the name of the location.
	Name string
	// ID is the unique ID assigned to this Location.
	// When [Location.Type] is [LocationTypeTitle], this represents the title ID.
	ID string `json:"Id"`
	// Type indicates the type of the location.
	// It is one of the constants below.
	Type int
}

const (
	// LocationTypeTitle indicates that the Location is a title.
	LocationTypeTitle = 3
)

// Action represents an action that can be taken on a notification.
type Action struct {
	// Actor is the user associated with this action.
	// It is not necessarily the caller. For game invites, this represents the
	// user who have sent the invite.
	Actor Actor
	// ActionID is the ID assigned to this action.
	ActionID string `json:"ActionId"`
	// Timestamp is the time at which this action was created.
	Timestamp time.Time `json:"ActionTime"`
	// Category is the subscription category of this action's parent notification.
	Category string `json:"SubscriptionCategory"`
	// Type is the subscription type of this action's parent notification.
	Type string `json:"SubscriptionType"`
	// ID is the subscription ID of this action's parent notification.
	ID string `json:"SubscriptionId"`
}

// Actor represents the user or entity associated with an Action.
type Actor struct {
	// ClassicGamerTag is the user's display name.
	ClassicGamerTag string `json:"ClassicGamertag"`
	// ModernGamerTag is the base portion of a modern gamertag, excluding
	// the numeric suffix.
	ModernGamerTag string `json:"ModernGamertag"`
	// ModernGamerTagSuffix is the numeric suffix of a modern gamertag,
	// without the hash character.
	ModernGamerTagSuffix string `json:"ModernGamertagSuffix"`
	// UniqueModernGamerTag is the fully qualified modern gamertag in the
	// format "[Actor.ModernGamerTag]#[Actor.ModernGamerTagSuffix]".
	UniqueModernGamerTag string `json:"UniqueModernGamertag"`

	// Name is the name of the actor.
	Name string
	// ID is the unique ID assigned to this actor.
	// When [Actor.Type] is [ActorTypeUser], this is the XUID of the user.
	ID string `json:"Id"`
	// Type indicates the type of the Actor.
	// It is one of the constants below.
	Type int
}

const (
	// ActorTypeUser indicates that an Actor is a user.
	ActorTypeUser = 1
)
