package rta

import (
	"encoding/json"
	"sync"
)

// Subscription represents a subscription contracted with the resource URI available through
// the real-time activity service. A Subscription may be contracted via Conn.Subscribe.
type Subscription struct {
	// ID is the ID assigned when the Subscription is first established.
	// It is retained for compatibility and diagnostics. If the Conn reconnects,
	// the active service ID may change; Conn methods route through the current
	// internal ID instead of this field.
	ID uint32
	// Custom is the custom data received when the Subscription is first established.
	//
	// The format and semantics of this data depend on the resource the
	// subscription is targeting. It is received alongside the successful
	// subscription response when the Subscription was established. The custom
	// data may change if the Conn has reconnected to RTA service; use
	// [Subscription.CurrentCustom] to read the latest value.
	Custom json.RawMessage

	currentID     uint32
	currentCustom json.RawMessage
	currentSet    bool
	active        bool
	resourceURI   string

	h    SubscriptionHandler
	hSet bool
	mu   sync.RWMutex
	// pending holds events delivered after the subscription is registered but
	// before the caller installs a handler on it.
	pending []json.RawMessage

	// eventQueue serializes handler callbacks off the WebSocket reader goroutine.
	eventMu      sync.Mutex
	eventQueue   []json.RawMessage
	eventRunning bool
}

// Active reports whether the subscription is currently registered with the
// Conn. A subscription becomes inactive after a successful unsubscribe or after
// reconnect gives up re-establishing it.
func (s *Subscription) Active() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

// ResourceURI returns the resource URI used to create the subscription.
func (s *Subscription) ResourceURI() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resourceURI
}

// activate records the latest service-side subscription state.
func (s *Subscription) activate(id uint32, custom json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ID == 0 {
		s.ID = id
		s.Custom = append(json.RawMessage(nil), custom...)
	}
	s.currentID = id
	s.currentCustom = append(json.RawMessage(nil), custom...)
	s.currentSet = true
	s.active = true
}

// deactivate marks the subscription as no longer registered.
func (s *Subscription) deactivate() {
	s.mu.Lock()
	s.active = false
	s.mu.Unlock()
}

// id returns the active service-side subscription ID. It may differ from the
// exported ID field after RTA reconnects and resubscribes.
func (s *Subscription) id() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.currentSet {
		return s.currentID
	}
	return s.ID
}

// custom returns the active custom payload. It may differ from the exported
// Custom field after RTA reconnects and resubscribes.
func (s *Subscription) custom() json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.currentSet {
		return append(json.RawMessage(nil), s.currentCustom...)
	}
	return append(json.RawMessage(nil), s.Custom...)
}

// CurrentCustom returns the current custom data associated with the
// Subscription. Unlike the exported Custom field, this value is updated when
// the subscription is re-established after an RTA reconnect.
func (s *Subscription) CurrentCustom() json.RawMessage {
	return s.custom()
}

// Handle registers a [SubscriptionHandler] on the [Subscription] to handle
// future events that may occur in the subscription. If h is nil, a no-op
// handler is registered.
func (s *Subscription) Handle(h SubscriptionHandler) {
	if h == nil {
		h = NopSubscriptionHandler{}
	}
	s.mu.Lock()
	s.h = h
	s.hSet = true
	pending := s.pending
	s.pending = nil
	s.enqueueEventsLocked(pending)
	s.mu.Unlock()
}

// handler returns the [SubscriptionHandler] currently registered on the [Subscription].
func (s *Subscription) handler() SubscriptionHandler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.hSet || s.h == nil {
		return NopSubscriptionHandler{}
	}
	return s.h
}

// dispatchEvent delivers an event to the installed handler, or briefly buffers
// it if the caller has not yet called Handle on a newly returned subscription.
func (s *Subscription) dispatchEvent(custom json.RawMessage) {
	custom = append(json.RawMessage(nil), custom...)
	s.mu.Lock()
	if !s.hSet {
		if len(s.pending) < maxPendingEventsBeforeHandler {
			s.pending = append(s.pending, custom)
		}
		s.mu.Unlock()
		return
	}
	s.enqueueEventsLocked([]json.RawMessage{custom})
	s.mu.Unlock()
}

// enqueueEventsLocked appends events to the per-subscription callback queue.
// The caller must hold s.mu so pending events are queued before live events that
// arrive immediately after Handle installs the handler.
func (s *Subscription) enqueueEventsLocked(events []json.RawMessage) {
	if len(events) == 0 {
		return
	}
	s.eventMu.Lock()
	for _, event := range events {
		s.eventQueue = append(s.eventQueue, append(json.RawMessage(nil), event...))
	}
	if !s.eventRunning {
		s.eventRunning = true
		go s.drainEvents()
	}
	s.eventMu.Unlock()
}

// drainEvents serializes user callbacks without blocking the WebSocket reader.
func (s *Subscription) drainEvents() {
	for {
		s.eventMu.Lock()
		if len(s.eventQueue) == 0 {
			s.eventRunning = false
			s.eventMu.Unlock()
			return
		}
		event := s.eventQueue[0]
		copy(s.eventQueue, s.eventQueue[1:])
		s.eventQueue[len(s.eventQueue)-1] = nil
		s.eventQueue = s.eventQueue[:len(s.eventQueue)-1]
		s.eventMu.Unlock()

		s.handler().HandleEvent(event)
	}
}

// SubscriptionHandler is the interface for handling events that may occur in a single
// [Subscription]. An implementation can be registered on a Subscription via [Subscription.Handle].
type SubscriptionHandler interface {
	// HandleEvent handles an event message received over the RTA subscription.
	// The event data reflects what occurred within that subscription.
	// For example, in Social API, an event is received when a user adds or
	// removes the caller.
	HandleEvent(custom json.RawMessage)
}

// ReconnectHandler is an optional extension interface for subscriptions that
// need to react when the Conn has reconnected to the RTA service and the
// subscription has been re-established on the new connection.
//
// If err is non-nil, the re-subscribe has failed. In this case, the
// [Subscription] still holds the ID and custom data from the previous
// connection. The handler does not need to call [Conn.Unsubscribe].
//
// If err is nil, the Subscription was successfully re-established on the new
// connection and has been assigned a new ID. This callback is fired as soon as
// that re-subscribe handshake succeeds. The custom data may also differ from
// the previous connection depending on the targeting resource. In this case,
// the handler remains responsible for calling [Conn.Unsubscribe] during cleanup.
type ReconnectHandler interface {
	HandleReconnect(err error)
}

// ResyncHandler is an optional extension interface for subscriptions that need
// to react to an RTA resync message. A resync message indicates that data may
// have been lost and the subscription's backing resource should be refreshed
// through its corresponding REST API.
type ResyncHandler interface {
	HandleResync()
}

// NopSubscriptionHandler is a no-op implementation of [SubscriptionHandler].
type NopSubscriptionHandler struct{}

func (NopSubscriptionHandler) HandleEvent(json.RawMessage) {}
