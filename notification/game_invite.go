package notification

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/df-mc/go-xsapi/v2/mpsd"
	"github.com/google/uuid"
)

type (
	// GameInvite represents a notification received when the caller is invited
	// to a game. The caller can join the multiplayer session by using the HandleID
	// contained in its Actions.
	GameInvite struct {
		notification[GameInviteAction]
		// Options contains options for launching/activating a title with the
		// invitation.
		Options GameInviteOptions `json:"NotificationOptions"`
	}

	// GameInviteAction represents an action that can be taken on a GameInvite
	// notification.
	GameInviteAction struct {
		Action

		// LaunchInfo contains information for launching/activating the title with the invite.
		LaunchInfo GameInviteLaunchInfo
	}

	// GameInviteOptions represents the options for a GameInvite notification.
	GameInviteOptions struct {
		// Location describes the title ta which the invite could be accepted.
		// To accept invitations for a specific title only, filter [Location.ID]
		// by the title ID.
		Location Location
		// Platforms lists the platforms supported by the game.
		Platforms []string
	}

	// GameInviteLaunchInfo holds the parameter required to launch the title
	// and join the multiplayer session from a GameInviteAction.
	GameInviteLaunchInfo struct {
		// HandleID is the ID corresponding to a handle within the
		// Multiplayer Session Directory (MPSD). Callers can use this ID as the second
		// parameter for [github.com/df-mc/go-xsapi/v2/mpsd.Client.Join] to join the multiplayer
		// session from the invitation.
		HandleID uuid.UUID `json:"mpsdHandleId"`
		// ExpirationTime indicates the time at which the handle identified
		// by [GameInviteLaunchInfo.HandleID] will expire.
		ExpirationTime time.Time `json:"expirationTime"`
		// GameTypes is a map whose keys are platform name such as "uwp-desktop" or
		// "android", and whose values are structs that describes a single title
		// associated with the invite handle.
		GameTypes map[string]mpsd.GameType `json:"gameTypes"`
	}
)

// UnmarshalJSON decodes the given JSON data into GameInviteLaunchInfo.
func (i *GameInviteLaunchInfo) UnmarshalJSON(b []byte) error {
	type Alias GameInviteLaunchInfo
	var data struct {
		*Alias
		// GameTypes contains an escaped-JSON struct that can be decoded to [mpsd.GameTypes].
		GameTypes string `json:"gameTypes"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(data.GameTypes), &i.GameTypes); err != nil {
		return fmt.Errorf("notification: decode GameInviteLaunchInfo.GameTypes: %w", err)
	}
	if i.GameTypes == nil {
		i.GameTypes = make(map[string]mpsd.GameType)
	}
	return nil
}
