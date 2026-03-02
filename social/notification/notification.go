package notification

import (
	"encoding/json"
	"time"
)

type Inbox struct {
	Items []PushNotification `json:"items"`
}

type PushNotification struct {
	Actions              []Action
	OtherActionCount     int // unknown
	MarkedRead           bool
	Seen                 bool
	HasToasted           bool
	NotificationOptions  json.RawMessage // unknown ({})
	Image                string
	ImageType            int // 1?
	InboxOptions         PushNotificationInboxOptions
	SubscriptionCategory string
	SubscriptionType     string
	ID                   string `json:"SubscriptionId"`
}

type PushNotificationOptions struct {
	Location  Location
	Platforms []string
}

type Location struct {
	PackageFamilyName string `json:"Pfn"`
	DisplayName       string
	Name              string
	ID                string `json:"Id"`
	Type              int
}

const (
	LocationTypeTitle = 3
)

type PushNotificationInboxOptions struct {
	CountOptions        PushNotificationCountOptions
	ImageOptions        PushNotificationImageOptions
	ExpiresAfterMinutes int
}

type PushNotificationCountOptions struct {
	InboxProvidesCount bool
	ResetCountOnRead   bool
}

type PushNotificationImageOptions struct {
	UseActorImage bool
}

type Action struct {
	ID                   string    `json:"ActionId"`
	Timestamp            time.Time `json:"ActionTime"`
	LaunchInfo           json.RawMessage
	SubscriptionCategory string
	SubscriptionType     string
	SubscriptionID       string `json:"SubscriptionId"`
}

const (
	SubscriptionCategoryPeople         = "Microsoft.Xbox.People"
	SubscriptionCategoryMultiplayer    = "Microsoft.Xbox.Multiplayer"
	SubscriptionCategoryRewards        = "Microsoft.Xbox.Rewards"
	SubscriptionCategoryStore          = "Microsoft.Xbox.Store"
	SubscriptionCategoryAchievements   = "Microsoft.Xbox.Achievements"
	SubscriptionCategoryTypeUGNConsent = "UGN.Consent"
)

const (
	SubscriptionTypeFollowers = "Followers"

	SubscriptionTypeGameInvites = "GameInvites"
	// SubscriptionTypeGamePartyInvites = "GamePartyInvites" // untested

	SubscriptionTypeMultiplayerActivityGameInvites = "MultiplayerActivityGameInvites"

	// SubscriptionTypeClaimReminder                       = "ClaimReminder"
	// SubscriptionTypeWishlistSaleDetailsPC               = "WishlistSaleDetailsPC"
	// SubscriptionTypeWishlistPreorderDetailsPC           = "WishlistPreorderDetailsPC"
	// SubscriptionTypeWishlistGameEntersGamePassDetailsPC = "WishlistGameEntersGamePassDetailsPC"
	// SubscriptionTypePriceIncrease = "PriceIncrease"

	// SubscriptionTypeAchievementUnlock = "AchievementUnlock"

	SubscriptionTypeAcceptedFriendRequests = "AcceptedFriendRequests"
	SubscriptionTypeIncomingFriendRequests = "IncomingFriendRequests"
)

type Actor struct {
	ClassicGamerTag      string `json:"ClassicGamertag"`
	ModernGamerTag       string `json:"ModernGamertag"`
	ModernGamerTagSuffix string `json:"ModernGamertagSuffix"`
	UniqueModernGamerTag string `json:"UniqueModernGamertag"`
	Name                 string
	ID                   string `json:"Id"`
	Type                 int
}

const (
	ActorTypeUser = 1
)
