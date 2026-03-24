package presence

import "time"

type Presence struct {
	// XUID is the XUID of the user associated with the Presence.
	XUID string `json:"xuid"`
	// Devices lists devices that the user is currently active on.
	Devices  []Device `json:"devices"`
	State    string   `json:"state"`
	LastSeen LastSeen `json:"lastSeen"`
}

type LastSeen struct {
	DeviceType string    `json:"deviceType"`
	TitleID    uint32    `json:"titleId"`
	TitleName  string    `json:"titleName"`
	Timestamp  time.Time `json:"timestamp"`
}

type Device struct {
	// Type indicates the type of the device.
	// Possible values include "D", "Xbox360", "MoLIVE", "WindowsPhone", "WindowsPhone7", and "PC".
	// For Android and iOS and other web-based titles, it will be "Web".
	Type string `json:"type"`

	Titles []Title `json:"titles"`
}

type Title struct {
	ID       uint32   `json:"id"`
	Name     string   `json:"name"`
	Activity Activity `json:"activity"`
}

type Activity struct {
	RichPresence string `json:"richPresence"`
	Media        Media  `json:"media"`
}

type Media struct {
	ID     string `json:"id"`
	IDType string `json:"idType"`
	Name   string `json:"name"`
}
