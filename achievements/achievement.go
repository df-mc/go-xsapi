// Package achievements reads Xbox Live achievement definitions and user progress.
package achievements

// Achievement contains a title's localized achievement metadata and user progress.
type Achievement struct {
	// ID identifies the achievement within its service configuration.
	ID string `json:"id"`
	// ServiceConfigID identifies the service configuration that owns the achievement.
	ServiceConfigID string `json:"serviceConfigId"`
	// Name is the localized display name.
	Name string `json:"name"`
	// Description is the localized description shown after unlocking the achievement.
	Description string `json:"description"`
	// LockedDescription is the localized description shown before unlocking the achievement.
	LockedDescription string `json:"lockedDescription"`
	// ProgressState is Achieved, InProgress, or NotStarted. Unknown values are
	// preserved so callers can handle new service states.
	ProgressState string `json:"progressState"`
	// Progression contains the user's progress and unlock time.
	Progression Progression `json:"progression"`
	// MediaAssets contains artwork associated with the achievement.
	MediaAssets []MediaAsset `json:"mediaAssets"`
	// Rewards contains the rewards granted for unlocking the achievement.
	Rewards []Reward `json:"rewards"`
}

// Progression describes the user's progress toward an achievement.
type Progression struct {
	// Requirements contains the conditions needed to unlock the achievement.
	Requirements []Requirement `json:"requirements"`
	// TimeUnlocked is the service's timestamp, preserved without parsing so an
	// absent or unusual timestamp does not discard the rest of the achievement.
	TimeUnlocked string `json:"timeUnlocked"`
}

// Requirement contains the current and target values for an achievement condition.
// Values are strings because their format depends on the title's configuration.
type Requirement struct {
	// ID identifies the condition within the achievement.
	ID string `json:"id"`
	// Current is the user's current progress toward the condition.
	Current string `json:"current"`
	// Target is the value required to complete the condition.
	Target string `json:"target"`
}

// MediaAsset identifies artwork associated with an achievement or reward.
type MediaAsset struct {
	// Name is the asset's display name.
	Name string `json:"name"`
	// Type describes the asset, such as Icon or Art.
	Type string `json:"type"`
	// URL is the location of the asset.
	URL string `json:"url"`
}

// Reward describes a reward such as Gamerscore, InApp content, or Art.
type Reward struct {
	// Name is the localized display name, if supplied by the service.
	Name string `json:"name"`
	// Description is the localized reward description, if supplied by the service.
	Description string `json:"description"`
	// Type identifies the reward category, such as Gamerscore, InApp, or Art.
	Type string `json:"type"`
	// Value is the reward's value as supplied by the service. Consumers decide how
	// to handle missing, malformed, or overflowing values without an invented score.
	Value string `json:"value"`
	// ValueType describes how to interpret Value, such as Int or String.
	ValueType string `json:"valueType"`
	// MediaAsset contains artwork for the reward, or nil when none is supplied.
	MediaAsset *MediaAsset `json:"mediaAsset"`
}

// PageResult is one response from the Xbox Live achievements service.
type PageResult struct {
	// Achievements contains this page's entries in service order.
	Achievements []Achievement `json:"achievements"`
	// PagingInfo describes the remaining pages.
	PagingInfo PagingInfo `json:"pagingInfo"`
}

// PagingInfo describes how to retrieve the next page of achievements.
type PagingInfo struct {
	// ContinuationToken is empty when there are no more pages.
	ContinuationToken string `json:"continuationToken"`
	// TotalRecords is the total number of matching entries reported by the service.
	TotalRecords int64 `json:"totalRecords"`
}
