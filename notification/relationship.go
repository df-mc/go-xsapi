package notification

// Each type below is defined as a struct embedding notification[Action] rather than
// a type alias. This gives each of them a distinct type, which allows callers to
// distinguish between them in a type switch.

type (
	// Followers represents a notification received when the caller is
	// followed by someone.
	Followers struct {
		notification[Action]
	}
	// AcceptedFriendRequests represents a notification received when friend
	// requests sent by the caller is accepted. [Actor.ID] in Actions
	// identifies who the caller has become friends with.
	AcceptedFriendRequests struct {
		notification[Action]
	}
	// IncomingFriendRequests represents a notification received when the
	// caller receives friend requests from someone. [Actor.ID] in Actions
	// identifies who sent the friend request.
	IncomingFriendRequests struct {
		notification[Action]
	}
)
