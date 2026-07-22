package notification

import "github.com/google/uuid"

type (
	// GameInvite represents a notification received when the caller is invited
	// to a game. The caller can join the multiplayer session by using the HandleID
	// contained in its Actions.
	GameInvite struct {
		notification[GameInviteAction]
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
	}
)
