package presence

import "time"

// Presence holds the online presence of a single user, including the devices
// they are currently active on and the titles they are playing.
//
// See: https://learn.microsoft.com/en-us/gaming/gdk/docs/reference/live/rest/json/json-presencerecord
type Presence struct {
	// XUID is the Xbox User ID (XUID) of the user.
	XUID string `json:"xuid"`
	// Devices lists the devices the user is currently active on.
	Devices []Device `json:"devices"`
	// State is the user's current activity on Xbox Live.
	// Not to be confused with [TitleRequest.State].
	//
	// Possible values are:
	//   - "Online": the user has at least one active device record.
	//   - "Away": the user is signed in but not active in any title.
	//   - "Offline": the user is not present on any device.
	State string `json:"state"`
	// LastSeen describes the last title the user was seen playing. It may be
	// nil if the record has been evicted from the Xbox Live cache.
	LastSeen *LastSeen `json:"lastSeen"`
}

// LastSeen describes the last title a user was seen playing.
//
// See: https://learn.microsoft.com/en-us/gaming/gdk/docs/reference/live/rest/json/json-lastseenrecord
type LastSeen struct {
	// DeviceType is the type of device the user was last seen on.
	// It holds the same set of values as [Device.Type].
	DeviceType string `json:"deviceType"`
	// TitleID is the ID of the last title the user was seen playing.
	TitleID uint32 `json:"titleId"`
	// TitleName is the localized display name of the title. The language
	// is determined by the "Accept-Language" header set by the caller.
	// See [github.com/df-mc/go-xsapi.AcceptLanguage].
	TitleName string `json:"titleName"`
	// Timestamp is when the user was last seen in the title.
	Timestamp time.Time `json:"timestamp"`
}

// Device holds the presence of a user on a single device. A user may be
// active in multiple titles on the same device simultaneously.
//
// See: https://learn.microsoft.com/en-us/gaming/gdk/docs/reference/live/rest/json/json-devicerecord
type Device struct {
	// Type is the device type. Known values are "D" (Xbox One/Series),
	// "Xbox360", "MoLIVE" (Windows), "WindowsPhone", "WindowsPhone7",
	// "PC" (Games for Windows Live), and "Web" (iOS, Android, or browser).
	Type string `json:"type"`
	// Titles lists the titles currently active on this device.
	Titles []Title `json:"titles"`
}

// Title represents a title a user is currently active in on a [Device].
//
// See: https://learn.microsoft.com/en-us/gaming/gdk/docs/reference/live/rest/json/json-titlerecord
type Title struct {
	// ID is the Title ID.
	ID uint32 `json:"id"`
	// Name is the localized, user-facing name of the title.
	// The language is determined by the "Accept-Language" header
	// set by the caller.
	// See [github.com/df-mc/go-xsapi.AcceptLanguage].
	Name string `json:"name"`
	// Activity holds detailed information about what the user is doing in
	// this title, such as their rich presence string or currently playing media.
	Activity Activity `json:"activity"`
}

// Activity holds detailed presence information for a title, such as
// a rich presence string or the media the user is currently playing.
//
// See: https://learn.microsoft.com/en-us/gaming/gdk/docs/reference/live/rest/json/json-activityrecord
type Activity struct {
	// RichPresence is the localized rich presence string for the title.
	// It is only populated for titles that support rich presence. The language
	// is determined by the "Accept-Language" header set by the caller.
	// See [github.com/df-mc/go-xsapi.AcceptLanguage].
	RichPresence string `json:"richPresence"`
	// Media describes the media the user is currently playing. It is only
	// populated for titles that report media activity, such as Spotify.
	Media *Media `json:"media"`
}

// Media describes a media item a user is currently playing.
//
// See: https://learn.microsoft.com/en-us/gaming/gdk/docs/reference/live/rest/json/json-mediarecord
type Media struct {
	// ID is the identifier for this media.
	// The format and semantics of this field depends on IDType.
	ID string `json:"id"`
	// IDType indicates how the ID field should be interpreted.
	// Possible values include "bing" and "provider".
	IDType string `json:"idType"`
	// Name is the localized, user-facing name of the media item. The language
	// of this text depends on the "Accept-Language" header set by the caller.
	// See [github.com/df-mc/go-xsapi.AcceptLanguage].
	Name string `json:"name"`
}
