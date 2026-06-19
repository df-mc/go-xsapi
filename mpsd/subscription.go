package mpsd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/google/uuid"
)

// subscribe subscribes with the RTA (Real-Time Activity) Services in Xbox Live.
// The subscription is used to associate with a multiplayer session to receive
// notifications for changes in the session.
func (c *Client) subscribe(ctx context.Context) (uuid.UUID, error) {
	c.subscribeMu.Lock()
	defer c.subscribeMu.Unlock()

	if err := c.subscriber.Subscribe(ctx, c.subscription); err != nil {
		return uuid.Nil, fmt.Errorf("mpsd: subscribe to %q: %w", resourceURI, err)
	}
	if data := c.subscriptionData.Load(); data != nil && data.ConnectionID != uuid.Nil {
		return data.ConnectionID, nil
	}
	return uuid.Nil, errors.New("mpsd: missing RTA connection ID")
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

	rta.NopSubscriptionHandler
}

func (h *subscriptionHandler) HandleSubscribe(custom json.RawMessage) error {
	h.reconcileMu.Lock()
	defer h.reconcileMu.Unlock()

	// The custom data includes a connection ID which can be used for the
	// Connection field in the member constants for receiving notifications for
	// the changes to its participating multiplayer session.
	var data subscriptionData
	if err := json.Unmarshal(custom, &data); err != nil {
		// If we return this error here, the Subscribe() call on rta.Conn will fail.
		return fmt.Errorf("parse subscription data: %w", err)
	}
	if data.ConnectionID == uuid.Nil {
		return errors.New("missing RTA connection ID in subscription data")
	}
	h.subscriptionData.Store(&data)

	h.log.Debug("received subscription data", "connectionID", data.ConnectionID)

	sessions := h.sessionSnapshot()
	var wg sync.WaitGroup
	for _, session := range sessions {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
			defer cancel()
			if err := reconcileSessionConnection(ctx, session, data.ConnectionID); err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				session.log.Error("error updating connection ID", "err", err)
				if closeErr := session.Close(); closeErr != nil {
					session.log.Error("error closing session after connection ID update failure", "err", closeErr)
					session.closeMu.Lock()
					session.closeLocked()
					session.closeMu.Unlock()
				}
				return
			}
		})
	}

	wg.Wait()
	return nil
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
		ref, err := h.parseReference(tap.Resource)
		if err != nil {
			h.log.Error("error parsing resource identifier in subscription event as session reference",
				slog.Any("error", err), slog.String("resource", tap.Resource))
			continue
		}
		refs = append(refs, ref)
	}

	for _, session := range h.sessionSnapshot() {
		if slices.ContainsFunc(refs, func(reference SessionReference) bool {
			// Shoulder taps may deliver TemplateName and Name in lowercase,
			// so use Compare for case-insensitive matching.
			return reference.Equal(session.Reference())
		}) {
			go func() {
				ctx, cancel := context.WithTimeout(session.Context(), time.Second*15)
				defer cancel()

				if err := h.syncSession(ctx, session); err != nil {
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
			}()
		}
	}
}

func (h *subscriptionHandler) HandleResync() {
	sessions := h.sessionSnapshot()
	var wg sync.WaitGroup
	for _, session := range sessions {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
			defer cancel()
			if err := h.syncSession(ctx, session); err != nil {
				h.log.Error("error resyncing multiplayer session", slog.Any("err", err))
				return
			}
			session.handler().HandleSessionChange(session)
		})
	}
	wg.Wait()
}

// syncSession synchronizes session while ordered against subscription
// reconciliation, but returns before user callbacks are invoked.
func (h *subscriptionHandler) syncSession(ctx context.Context, session *Session) error {
	h.reconcileMu.RLock()
	defer h.reconcileMu.RUnlock()
	return session.Sync(ctx)
}

func (h *subscriptionHandler) HandleError(err error) {
	for _, session := range h.sessionSnapshot() {
		// TODO: Cancel the background context of the session.
		session.log.Error("subscription lost", "err", err)
		go func() {
			h.reconcileMu.RLock()
			defer h.reconcileMu.RUnlock()

			if closeErr := session.Close(); closeErr != nil {
				session.log.Error("error closing session after subscription loss", "err", closeErr)
				session.closeMu.Lock()
				session.closeLocked()
				session.closeMu.Unlock()
			}
		}()
	}
}

// sessionSnapshot returns the currently tracked sessions without keeping
// sessionsMu held while callers perform network or close operations.
func (h *subscriptionHandler) sessionSnapshot() []*Session {
	h.sessionsMu.RLock()
	defer h.sessionsMu.RUnlock()

	sessions := make([]*Session, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	return sessions
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
