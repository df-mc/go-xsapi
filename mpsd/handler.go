package mpsd

import "github.com/google/uuid"

// Handler notifies that a Session has been changed. It is called by the handler of
// *rta.Subscription contracted with *rta.Conn on [PublishConfig.PublishContext].
type Handler interface {
	// HandleSessionChange handles a change of session. The latest state of Session can be
	// retrieved via [Session.Query].
	HandleSessionChange(ref SessionReference, branch uuid.UUID, changeNumber uint64)
}

// A NopHandler implements a no-op Handler, which does nothing.
type NopHandler struct{}

func (NopHandler) HandleSessionChange(SessionReference, uuid.UUID, uint64) {}

// Handle stores a Handler into the Session atomically, which notifies events that may occur
// in the *rta.Subscription of the Session. If Handler is a nil, a NopHandler will be stored
// instead.
func (s *Session) Handle(h Handler) {
	if h == nil {
		h = NopHandler{}
	}
	s.h.Store(&h)
}

// handler returns the Handler of the Session. It is usually called to handle events that may occur.
func (s *Session) handler() Handler {
	return *s.h.Load()
}
