// Package achievements reads Xbox Live achievement definitions and user progress.
package achievements

// Achievement contains a title's localized achievement metadata and user progress.
type Achievement struct {
	ID                string `json:"id"`
	ServiceConfigID   string `json:"serviceConfigId"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	LockedDescription string `json:"lockedDescription"`
	// ProgressState is Achieved, InProgress, or NotStarted. Unknown values are
	// preserved so callers can handle new service states.
	ProgressState string       `json:"progressState"`
	Progression   Progression  `json:"progression"`
	MediaAssets   []MediaAsset `json:"mediaAssets"`
	Rewards       []Reward     `json:"rewards"`
}

// Progression describes the user's progress toward an achievement.
type Progression struct {
	Requirements []Requirement `json:"requirements"`
	// TimeUnlocked is the service's timestamp, preserved without parsing so an
	// absent or unusual timestamp does not discard the rest of the achievement.
	TimeUnlocked string `json:"timeUnlocked"`
}

// Requirement contains the current and target values for an achievement condition.
// Values are strings because their format depends on the title's configuration.
type Requirement struct {
	ID      string `json:"id"`
	Current string `json:"current"`
	Target  string `json:"target"`
}

// MediaAsset identifies artwork associated with an achievement or reward.
type MediaAsset struct {
	Name string `json:"name"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

// Reward describes a reward such as Gamerscore, InApp content, or Art.
// Value is kept as a string; consumers decide how to handle missing, malformed,
// or overflowing values rather than receiving an invented numeric score.
type Reward struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Type        string      `json:"type"`
	Value       string      `json:"value"`
	ValueType   string      `json:"valueType"`
	MediaAsset  *MediaAsset `json:"mediaAsset"`
}

// Page is one response from the Xbox Live achievements service.
type Page struct {
	Achievements []Achievement `json:"achievements"`
	PagingInfo   PagingInfo    `json:"pagingInfo"`
}

// PagingInfo describes how to retrieve the next page of achievements.
type PagingInfo struct {
	// ContinuationToken is empty when there are no more pages.
	ContinuationToken string `json:"continuationToken"`
	TotalRecords      int64  `json:"totalRecords"`
}
