package mpsd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/google/uuid"
)

// subscribe subscribes with the RTA (Real-Time Activity) Services in Xbox Live.
// The subscription is used to associate with a multiplayer session to receive
// notifications for changes in the session.
func (c *Client) subscribe(ctx context.Context) (_ *rta.Subscription, _ *subscriptionData, err error) {
	if err := c.closingErr(); err != nil {
		return nil, nil, err
	}
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	if err := c.closingErr(); err != nil {
		return nil, nil, err
	}
	oldData := c.subscriptionData
	if c.subscription != nil && oldData != nil {
		custom := c.subscription.CurrentCustom()
		var data subscriptionData
		if err := json.Unmarshal(custom, &data); err != nil {
			c.resetBrokenSubscription(c.subscription)
			c.subscription, c.subscriptionData = nil, nil
			return nil, nil, fmt.Errorf("mpsd: subscribe to %q: decode subscription custom: %w", resourceURI, err)
		}
		if data.ConnectionID == uuid.Nil {
			c.resetBrokenSubscription(c.subscription)
			c.subscription, c.subscriptionData = nil, nil
			return nil, nil, fmt.Errorf("mpsd: subscribe to %q: invalid subscription data: %q", resourceURI, custom)
		}
		c.subscriptionData = &data
		if oldData.ConnectionID != data.ConnectionID {
			(&subscriptionHandler{Client: c}).refreshSessionConnections(data.ConnectionID)
		}
		// If the subscription was already made with RTA, return the cached
		// subscription along with its refreshed decoded payload.
		return c.subscription, &data, nil
	}

	defer func() {
		if err != nil {
			c.resetBrokenSubscription(c.subscription)
			c.subscription, c.subscriptionData = nil, nil
		}
	}()

	c.subscription, err = c.rta.Subscribe(ctx, resourceURI)
	if err != nil {
		return nil, nil, fmt.Errorf("mpsd: subscribe to %q: %w", resourceURI, err)
	}
	// The custom data includes a connection ID which can be used for the
	// Connection field in the member constants for receiving notifications for
	// the changes to its participating multiplayer session.
	if err := json.Unmarshal(c.subscription.CurrentCustom(), &c.subscriptionData); err != nil {
		return nil, nil, fmt.Errorf("mpsd: subscribe to %q: decode subscription custom: %w", resourceURI, err)
	}
	if c.subscriptionData == nil || c.subscriptionData.ConnectionID == uuid.Nil {
		return nil, nil, fmt.Errorf("mpsd: subscribe to %q: invalid subscription data: %q", resourceURI, c.subscription.CurrentCustom())
	}
	c.subscription.Handle(&subscriptionHandler{
		Client: c,
		log:    c.log.With("src", "subscription handler"),
	})
	if oldData == nil {
		(&subscriptionHandler{Client: c}).refreshSessionConnections(c.subscriptionData.ConnectionID)
	}
	return c.subscription, c.subscriptionData, nil
}

// resetBrokenSubscription unsubscribes a subscription whose custom payload
// cannot be trusted, then lets the client retry from a clean state later.
func (c *Client) resetBrokenSubscription(subscription *rta.Subscription) {
	unsub := c.unsub
	if subscription == nil || unsub == nil {
		return
	}
	// If the subscription was unsuccessful, reset the RTA state in the
	// background so a future subscribe can retry without leaking the old handle.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
		defer cancel()
		if err := unsub.Unsubscribe(ctx, subscription); err != nil {
			c.log.Error("error resetting broken subscription", slog.Any("error", err))
		}
	}()
}

// resourceURI is the resource URI used to subscribe with RTA (Real-Time Activity) Services
// in Xbox Live. The subscription is then used to associate with a multiplayer session to
// get updates in a WebSocket connection.
const resourceURI = "https://sessiondirectory.xboxlive.com/connections/"

// Handler receives session events delivered over an RTA (Real-Time Activity)
// subscription. It is primarily used to react to changes made to a remote
// session in the Multiplayer Session Directory.
//
// A Handler can be registered on a session via [Session.Handle].
// NopHandler provides a no-op implementation of Handler.
type Handler interface {
	// HandleSessionChange is called when a change is made to a remote session
	// in the directory. This includes events such as a member joining the
	// session or a custom property being updated. For the full list of changes
	// that trigger this handler, refer to the ChangeType* constants defined in
	// this package.
	HandleSessionChange(session *Session)
}

// NopHandler is a no-op implementation of [Handler]. It is used as the default
// handler for a Session. A custom implementation can be registered at any time
// via [Session.Handle].
type NopHandler struct{}

// HandleSessionChange implements [Handler.HandleSessionChange].
func (NopHandler) HandleSessionChange(*Session) {}

// subscriptionData describes a wire representation of the custom payload
// included in the RTA subscription.
type subscriptionData struct {
	// ConnectionID is used to associate the RTA subscription with a multiplayer
	// session. It can be used as the [MemberPropertiesSystem.Connection] field.
	ConnectionID uuid.UUID `json:"ConnectionId"`
}

// subscriptionHandler handles events received over an RTA subscription
// associated with a multiplayer session.
//
// It decodes subscription events, interprets shoulder taps, and extracts
// session references from resource identifiers included in the notifications
// in order to synchronize the session properties with the latest state.
type subscriptionHandler struct {
	*Client
	log *slog.Logger
}

// HandleEvent handles an event received over the RTA subscription associated
// with the multiplayer session.
//
// The event payload describes changes to the session, such as members joining
// or leaving, or updates to session or member properties.
func (h *subscriptionHandler) HandleEvent(custom json.RawMessage) {
	var event subscriptionEvent
	if err := json.Unmarshal(custom, &event); err != nil {
		h.log.Error("error decoding subscription event",
			slog.Any("error", err),
			slog.String("data", string(custom)),
		)
		return
	}

	if len(event.ShoulderTaps) == 0 {
		h.log.Debug("subscription event does not include any shoulder taps",
			slog.String("data", string(custom)))
		return
	}

	refs := make([]SessionReference, 0, len(event.ShoulderTaps))
	for _, tap := range event.ShoulderTaps {
		if !h.shouldProcessTap(tap) {
			continue
		}
		ref, err := h.parseReference(tap.Resource)
		if err != nil {
			h.log.Error("error parsing resource identifier in subscription event as session reference",
				slog.Any("error", err), slog.String("resource", tap.Resource))
			continue
		}
		refs = append(refs, ref)
	}

	h.sessionsMu.RLock()
	for _, session := range h.sessions {
		if slices.ContainsFunc(refs, func(reference SessionReference) bool {
			// Shoulder taps may deliver TemplateName and Name in lowercase,
			// so use Compare for case-insensitive matching.
			return reference.Equal(session.Reference())
		}) {
			go func(s *Session) {
				h.syncSession(s)
			}(session)
		}
	}
	h.sessionsMu.RUnlock()
}

// shouldProcessTap reports whether tap is newer than the last processed
// shoulder tap for its branch. Taps without branch/change metadata are treated
// as processable because they cannot be deduplicated safely.
func (h *subscriptionHandler) shouldProcessTap(tap shoulderTap) bool {
	if tap.Branch == uuid.Nil || tap.ChangeNumber == 0 {
		return true
	}
	h.shoulderTapsMu.Lock()
	defer h.shoulderTapsMu.Unlock()
	if h.shoulderTaps == nil {
		h.shoulderTaps = make(map[uuid.UUID]uint64)
	}
	last, ok := h.shoulderTaps[tap.Branch]
	if ok && tap.ChangeNumber <= last {
		return false
	}
	h.shoulderTaps[tap.Branch] = tap.ChangeNumber
	return true
}

// HandleResync implements [rta.ResyncHandler].
func (h *subscriptionHandler) HandleResync() {
	h.sessionsMu.RLock()
	sessions := make([]*Session, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.sessionsMu.RUnlock()

	for _, session := range sessions {
		go h.syncSession(session)
	}
}

// syncSession refreshes one tracked session from MPSD and notifies its handler
// if the refresh succeeds.
func (h *subscriptionHandler) syncSession(session *Session) {
	ctx, cancel := context.WithTimeout(session.Context(), time.Second*15)
	defer cancel()

	if err := session.Sync(ctx); err != nil {
		h.log.Error("error synchronizing multiplayer session",
			slog.Any("error", err))
		return
	}
	h.log.Debug("synchronized multiplayer session",
		slog.Group("session",
			slog.String("ref", session.Reference().URL().String()),
		),
	)
	session.handler().HandleSessionChange(session)
}

// HandleReconnect implements [rta.SubscriptionHandler].
func (h *subscriptionHandler) HandleReconnect(err error) {
	h.subscriptionMu.Lock()

	if err != nil {
		h.subscription, h.subscriptionData = nil, nil
		h.subscriptionMu.Unlock()
		return
	}
	if h.subscription == nil {
		h.subscriptionMu.Unlock()
		return
	}

	var data subscriptionData
	if err := json.Unmarshal(h.subscription.CurrentCustom(), &data); err != nil {
		h.logger().Error("error decoding reconnected subscription custom",
			slog.Any("error", err),
			slog.String("data", string(h.subscription.CurrentCustom())),
		)
		h.resetBrokenSubscription(h.subscription)
		h.subscription, h.subscriptionData = nil, nil
		h.subscriptionMu.Unlock()
		return
	}
	if data.ConnectionID == uuid.Nil {
		h.logger().Error("invalid reconnected subscription custom",
			slog.String("data", string(h.subscription.CurrentCustom())),
		)
		h.resetBrokenSubscription(h.subscription)
		h.subscription, h.subscriptionData = nil, nil
		h.subscriptionMu.Unlock()
		return
	}
	oldData := h.subscriptionData
	h.subscriptionData = &data
	h.subscriptionMu.Unlock()

	if oldData == nil || oldData.ConnectionID != data.ConnectionID {
		h.refreshSessionConnections(data.ConnectionID)
	}
}

// refreshSessionConnections rewrites every tracked session with a new RTA
// connection ID after the MPSD subscription is re-established.
func (h *subscriptionHandler) refreshSessionConnections(connectionID uuid.UUID) {
	h.sessionsMu.RLock()
	sessions := make([]*Session, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.sessionsMu.RUnlock()

	for _, session := range sessions {
		go h.refreshSessionConnection(session, connectionID)
	}
}

// refreshSessionConnection marks the local player active in session using the
// current MPSD RTA connection ID.
func (h *subscriptionHandler) refreshSessionConnection(session *Session, connectionID uuid.UUID) {
	ctx, cancel := context.WithTimeout(session.Context(), time.Second*15)
	defer cancel()

	deleted, err := session.update(ctx, SessionDescription{
		Members: map[string]*MemberDescription{
			"me": {
				Properties: &MemberProperties{
					System: &MemberPropertiesSystem{
						Active:     true,
						Connection: connectionID,
					},
				},
			},
		},
	}, nil)
	if err != nil {
		h.logger().Error("error updating multiplayer session connection ID",
			slog.Any("error", err),
			slog.Group("session",
				slog.String("ref", session.Reference().URL().String()),
			),
		)
		return
	}
	if deleted {
		session.markDeleted()
	}
}

// logger returns the handler logger, falling back to the owning client logger
// when the handler was constructed without one.
func (h *subscriptionHandler) logger() *slog.Logger {
	if h.log != nil {
		return h.log
	}
	if h.Client != nil && h.Client.log != nil {
		return h.Client.log
	}
	return slog.Default()
}

// parseReference parses a SessionReference from a resource identifier included
// in a shoulder tap received over an RTA subscription.
//
// The input string is expected to be formatted as:
//
//	[ServiceConfigID]~[TemplateName]~[SessionName]
//
// where the segments correspond to fields of SessionReference.
func (h *subscriptionHandler) parseReference(s string) (ref SessionReference, err error) {
	segments := strings.Split(s, "~")
	if len(segments) != 3 {
		return ref, fmt.Errorf("badly formatted session reference, must consist of three parts separated with '~' character: %q", s)
	}
	ref.ServiceConfigID, err = uuid.Parse(segments[0])
	if err != nil {
		return ref, fmt.Errorf("parse service config ID: %w", err)
	}
	ref.TemplateName, ref.Name = segments[1], segments[2]
	return ref, nil
}

// subscriptionEvent represents a subscription event received from Xbox Live
// Real-Time Activity (RTA) services.
//
// A subscription event contains one or more shoulder taps that indicate
// changes to resources in the Multiplayer Session Directory (MPSD).
type subscriptionEvent struct {
	// ShoulderTaps contains lightweight notifications indicating changes to
	// resources associated with the subscription.
	//
	// Each shoulder tap references a specific resource that has changed and
	// provides the change number and branch identifying the version of that
	// resource. The shoulder tap does not contain the updated resource data;
	// clients are expected to sync the resource to the latest state separately if needed.
	ShoulderTaps []shoulderTap `json:"shoulderTaps"`
}

// shoulderTap is a lightweight notification included in an RTA subscription
// event to indicate that a specific resource in the Multiplayer Session
// Directory (MPSD) has changed.
type shoulderTap struct {
	// Resource identifies the resource referenced by this notification.
	//
	// The identifier may consist of multiple segments delimited by '~'. For example,
	// parseSessionReference can be used to parse the resource identifier as a
	// reference to a multiplayer session.
	Resource string `json:"resource"`

	// ChangeNumber identifies the change number of the resource to which this
	// notification applies.
	ChangeNumber uint64 `json:"changeNumber"`

	// Branch is a unique identifier for the current branch of the resource.
	Branch uuid.UUID `json:"branch"`
}
