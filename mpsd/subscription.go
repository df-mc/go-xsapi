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

	"github.com/df-mc/go-xsapi/rta"
	"github.com/google/uuid"
)

// errSubscriptionUnavailable is returned when no RTA subscriber is configured
// and the subscription cannot be refreshed.
var errSubscriptionUnavailable = errors.New("mpsd: subscription unavailable")

// subscribe subscribes with the RTA (Real-Time Activity) Services in Xbox Live.
// The subscription is used to associate with a multiplayer session to receive
// notifications for changes in the session.
func (c *Client) subscribe(ctx context.Context) (_ *rta.Subscription, _ *subscriptionData, err error) {
	if c.wait != nil {
		if err := c.wait(ctx); err != nil {
			return nil, nil, err
		}
	}
	return c.subscribeWithInstall(ctx, c.backgroundInstallGate(c.backgroundSeq.Load()))
}

// subscribeWithInstall returns an active subscription, reusing the cached one
// when possible. If no subscription exists it fetches a new one and installs it
// on the Client. canInstall is checked before installing; when it returns false
// the in-flight subscription is discarded and [net.ErrClosed] is returned.
// Concurrent callers coalesce behind a single in-flight fetch via subscribeDone.
func (c *Client) subscribeWithInstall(ctx context.Context, canInstall func() bool) (_ *rta.Subscription, _ *subscriptionData, err error) {
	for {
		c.subscriptionMu.Lock()
		if c.subscription != nil {
			if c.active != nil && !c.active(c.subscription) {
				c.clearSubscriptionLocked()
			} else {
				data, err := c.decodeSubscriptionData(c.subscription)
				if err != nil {
					c.resetSubscriptionLocked(c.subscription)
					c.subscriptionMu.Unlock()
					return nil, nil, fmt.Errorf("mpsd: subscribe to %q: decode subscription custom: %w", resourceURI, err)
				}
				if !canInstall() {
					c.subscriptionMu.Unlock()
					return nil, nil, net.ErrClosed
				}
				c.subscriptionData = data
				subscription := c.subscription
				c.subscriptionMu.Unlock()
				// If the subscription was already made with RTA, return the cached
				// subscription along with its refreshed decoded payload.
				return subscription, data, nil
			}
		}

		if c.subscribeDone != nil {
			done := c.subscribeDone
			c.subscriptionMu.Unlock()
			select {
			case <-done:
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}

		done := make(chan struct{})
		c.subscribeDone = done
		c.subscriptionMu.Unlock()

		subscription, data, err := c.fetchSubscription(ctx)

		c.subscriptionMu.Lock()
		if c.subscribeDone == done {
			c.subscribeDone = nil
			close(done)
		}

		if err != nil {
			c.subscriptionMu.Unlock()
			if subscription != nil {
				c.cleanupSubscription(subscription)
			}
			return nil, nil, err
		}

		if !canInstall() {
			c.subscriptionMu.Unlock()
			c.cleanupSubscription(subscription)
			return nil, nil, net.ErrClosed
		}

		if c.subscription == nil {
			c.subscription = subscription
			c.subscriptionData = data
			c.subscription.Handle(&subscriptionHandler{
				Client:       c,
				subscription: subscription,
				log:          c.log.With("src", "subscription handler"),
			})
			c.subscriptionMu.Unlock()
			return subscription, data, nil
		}
		c.subscriptionMu.Unlock()
		c.cleanupSubscription(subscription)
	}
}

// fetchSubscription performs the RTA subscribe call and decodes the resulting
// subscription data. On decode failure the raw subscription is still returned
// so the caller can clean it up.
func (c *Client) fetchSubscription(ctx context.Context) (_ *rta.Subscription, _ *subscriptionData, err error) {
	if c.sub == nil {
		return nil, nil, errSubscriptionUnavailable
	}
	subscription, err := c.sub.Subscribe(ctx, resourceURI)
	if err != nil {
		return nil, nil, fmt.Errorf("mpsd: subscribe to %q: %w", resourceURI, err)
	}
	data, err := c.decodeSubscriptionData(subscription)
	if err != nil {
		return subscription, nil, fmt.Errorf("mpsd: subscribe to %q: decode subscription custom: %w", resourceURI, err)
	}
	return subscription, data, nil
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
	subscription *rta.Subscription
	log          *slog.Logger
	rta.NopSubscriptionHandler
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

	h.sessionsMu.RLock()
	for _, session := range h.sessions {
		if slices.Contains(refs, session.ref) {
			go func(s *Session) {
				ctx, cancel := context.WithTimeout(s.Context(), time.Second*15)
				defer cancel()

				if err := s.Sync(ctx); err != nil {
					h.log.Error("error synchronizing multiplayer session",
						slog.Any("error", err))
					return
				}
				h.log.Debug("synchronized multiplayer session",
					slog.Group("session",
						slog.String("ref", s.Reference().URL().String()),
					),
				)
				s.handler().HandleSessionChange(s)
			}(session)
		}
	}
	h.sessionsMu.RUnlock()
}

// HandleReconnect implements [rta.SubscriptionHandler].
func (h *subscriptionHandler) HandleReconnect(err error) {
	if err == nil {
		return
	}

	h.subscriptionMu.Lock()
	if h.handleStaleReconnectFailureLocked(err) {
		return
	}
	lossSeq := h.handleSubscriptionLossLocked(nil)
	h.subscriptionMu.Unlock()
	h.logSubscriptionLoss("error reconnecting MPSD subscription", err, lossSeq)
}

// handleStaleReconnectFailureLocked handles reconnect failure from a stale
// subscription handle while subscriptionMu is held. It returns true after
// consuming the failure path and releasing subscriptionMu.
func (h *subscriptionHandler) handleStaleReconnectFailureLocked(err error) bool {
	if h.subscription == nil || h.subscription == h.Client.subscription {
		return false
	}

	current := h.Client.subscription
	if current == nil {
		lossSeq := h.handleSubscriptionLossLocked(nil)
		h.subscriptionMu.Unlock()
		h.logSubscriptionLoss("error reconnecting MPSD subscription", err, lossSeq)
		return true
	}

	currentData, currentErr := h.decodeSubscriptionData(current)
	if currentErr != nil {
		lossSeq := h.handleSubscriptionLossLocked(current)
		h.subscriptionMu.Unlock()
		h.logSubscriptionLoss("error decoding replacement MPSD subscription after stale reconnect failure", currentErr, lossSeq)
		return true
	}

	oldData, oldErr := h.decodeSubscriptionData(h.subscription)
	backgroundSeq := h.backgroundSeq.Load()
	h.subscriptionMu.Unlock()
	if oldErr != nil || oldData == nil || oldData.ConnectionID != currentData.ConnectionID {
		h.startRefreshWaveIfCurrent(current, backgroundSeq, currentData.ConnectionID)
	}
	return true
}

// HandleReconnectReady implements [rta.ReconnectReadyHandler] so MPSD can
// rebind tracked sessions as soon as its own subscription has been refreshed.
func (h *subscriptionHandler) HandleReconnectReady() {
	h.handleReconnectSuccess()
}

// handleReconnectSuccess is called when the RTA connection has successfully
// re-established the subscription. It decodes the refreshed subscription data
// and starts a refresh wave if the connection ID changed.
func (h *subscriptionHandler) handleReconnectSuccess() {
	h.subscriptionMu.Lock()
	subscription := h.subscription
	if subscription == nil {
		subscription = h.Client.subscription
	}
	if subscription == nil || (h.subscription != nil && subscription != h.Client.subscription) {
		h.subscriptionMu.Unlock()
		return
	}
	data, err := h.decodeSubscriptionData(subscription)
	if err != nil {
		lossSeq := h.handleSubscriptionLossLocked(subscription)
		h.subscriptionMu.Unlock()
		h.logSubscriptionLoss("error decoding refreshed subscription data", err, lossSeq)
		return
	}
	prev := h.subscriptionData
	h.subscriptionData = data
	backgroundSeq := h.backgroundSeq.Load()
	h.subscriptionMu.Unlock()

	if prev == nil || prev.ConnectionID != data.ConnectionID {
		h.startRefreshWaveIfCurrent(subscription, backgroundSeq, data.ConnectionID)
	}
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

// decodeSubscriptionData unmarshals the custom payload of an RTA subscription
// into [subscriptionData] and validates the connection ID.
func decodeSubscriptionData(subscription *rta.Subscription) (*subscriptionData, error) {
	var data subscriptionData
	if err := json.Unmarshal(subscription.Custom(), &data); err != nil {
		return nil, err
	}
	if data.ConnectionID == uuid.Nil {
		return nil, fmt.Errorf("invalid subscription data: %q", subscription.Custom())
	}
	return &data, nil
}

// cleanupSubscription unsubscribes a discarded or failed RTA subscription in
// the background with a 15 second timeout.
func (c *Client) cleanupSubscription(subscription *rta.Subscription) {
	if subscription == nil || c.unsub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	if err := c.unsub.Unsubscribe(ctx, subscription); err != nil {
		c.log.Error("error resetting broken subscription", slog.Any("error", err))
	}
}

// clearSubscriptionLocked nils both the subscription and its decoded data.
// The caller must hold subscriptionMu.
func (c *Client) clearSubscriptionLocked() {
	c.subscription, c.subscriptionData = nil, nil
}

// resetSubscriptionLocked cancels any running refresh wave, clears the
// subscription state, and asynchronously cleans up the given subscription.
// The caller must hold subscriptionMu.
func (c *Client) resetSubscriptionLocked(subscription *rta.Subscription) {
	c.cancelRefreshWave()
	c.clearSubscriptionLocked()
	if subscription == nil || c.unsub == nil {
		return
	}
	go c.cleanupSubscription(subscription)
}

// cancelRefreshWave cancels the currently active refresh wave, if any.
func (c *Client) cancelRefreshWave() {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.cancelRefreshWaveLocked()
}

// cancelRefreshWaveLocked is cancelRefreshWave with refreshMu already held.
func (c *Client) cancelRefreshWaveLocked() {
	if c.refreshCancel != nil {
		c.refreshCancel()
		c.refreshCancel = nil
	}
	c.refreshingSeq = 0
}

// reconcileSessionConnectionWithInstall checks whether the session's
// connection ID still matches the current subscription data and, if not,
// refreshes it. canInstall is used by background goroutines that must stop
// when the Client is closing or their generation has been invalidated.
func (c *Client) reconcileSessionConnectionWithInstall(ctx context.Context, session *Session, connectionID uuid.UUID, canInstall func() bool) error {
	if c.closing.Load() {
		return net.ErrClosed
	}
	if c.wait != nil {
		if err := c.wait(ctx); err != nil {
			return err
		}
	}

	data, err := c.subscriptionDataWithInstall(ctx, canInstall)
	if err != nil {
		return err
	}
	if data.ConnectionID == connectionID {
		return nil
	}
	if err := session.refreshConnection(ctx, data.ConnectionID); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

// subscriptionDataWithInstall returns the current subscription data,
// re-establishing the subscription when the cached state is stale or missing.
func (c *Client) subscriptionDataWithInstall(ctx context.Context, canInstall func() bool) (*subscriptionData, error) {
	refresh := func() (*subscriptionData, error) {
		if c.closing.Load() {
			return nil, net.ErrClosed
		}
		if c.sub == nil {
			return nil, errSubscriptionUnavailable
		}
		_, data, err := c.subscribeWithInstall(ctx, canInstall)
		if err != nil {
			return nil, err
		}
		return data, nil
	}

	c.subscriptionMu.Lock()
	subscription := c.subscription
	if subscription == nil {
		c.subscriptionMu.Unlock()
		return refresh()
	}
	if c.active != nil && !c.active(subscription) {
		c.clearSubscriptionLocked()
		c.subscriptionMu.Unlock()
		return refresh()
	}
	data, err := c.decodeSubscriptionData(subscription)
	if err != nil {
		c.resetSubscriptionLocked(subscription)
		c.subscriptionMu.Unlock()
		return refresh()
	}
	if !canInstall() {
		c.subscriptionMu.Unlock()
		return nil, net.ErrClosed
	}
	c.subscriptionData = data
	c.subscriptionMu.Unlock()
	return data, nil
}

// retryReconcileSessionConnection retries reconciling a single session's
// connection ID with exponential backoff, giving up after five attempts.
func (c *Client) retryReconcileSessionConnection(session *Session, connectionID uuid.UUID, backgroundSeq uint64) {
	canInstall := c.backgroundInstallGate(backgroundSeq)
	for attempt := 0; ; attempt++ {
		if err := session.Context().Err(); err != nil {
			return
		}
		if !canInstall() || c.closing.Load() {
			session.markTrackingLost()
			return
		}

		ctx, cancel := context.WithTimeout(session.Context(), time.Second*15)
		err := c.reconcileSessionConnectionWithInstall(ctx, session, connectionID, canInstall)
		cancel()
		if err == nil {
			if !c.reattachSession(session, backgroundSeq) {
				session.log.Warn("automatic session tracking lost after late attach")
			}
			return
		}
		if errors.Is(err, net.ErrClosed) {
			session.markTrackingLost()
			return
		}

		session.log.Error("error reconciling session connection after reconnect", slog.Any("error", err))

		if attempt >= 4 {
			session.markTrackingLost()
			return
		}
		select {
		case <-time.After(reconnectBackoff(attempt)):
		case <-session.Context().Done():
			return
		}
	}
}

// refreshWaveActive reports whether the current refresh wave is still valid.
func (c *Client) refreshWaveActive(waveCtx context.Context, seq uint64) bool {
	return !c.closing.Load() && waveCtx.Err() == nil && c.refreshSequenceActive(seq)
}

// refreshSessionConnection writes the new connection ID to a single session,
// respecting both the session and wave lifetimes.
func (c *Client) refreshSessionConnection(session *Session, waveCtx context.Context, connectionID uuid.UUID, seq uint64) error {
	ctx, cancel := refreshContext(session.Context(), waveCtx)
	defer cancel()
	return session.refreshConnectionWhile(ctx, connectionID, func() bool {
		return c.refreshWaveActive(waveCtx, seq)
	})
}

// retryRefreshSessionConnection retries refreshing a single session's
// connection ID within a refresh wave, giving up after five attempts.
func (c *Client) retryRefreshSessionConnection(session *Session, waveCtx context.Context, connectionID uuid.UUID, seq uint64) {
	for attempt := 0; ; attempt++ {
		if err := session.Context().Err(); err != nil || !c.refreshWaveActive(waveCtx, seq) {
			return
		}

		err := c.refreshSessionConnection(session, waveCtx, connectionID, seq)
		if err == nil || errors.Is(err, net.ErrClosed) || refreshInterrupted(err, session.Context(), waveCtx) || !c.refreshSequenceActive(seq) {
			return
		}

		c.log.Error("error refreshing session connection ID after reconnect",
			slog.Any("error", err),
			slog.Group("session", slog.String("ref", session.Reference().URL().String())),
		)

		if attempt >= 4 {
			return
		}
		select {
		case <-time.After(reconnectBackoff(attempt)):
		case <-session.Context().Done():
		case <-waveCtx.Done():
			return
		}
	}
}

// refreshSessionConnections writes the new connection ID to all tracked
// sessions concurrently, retrying failures individually.
func (c *Client) refreshSessionConnections(waveCtx context.Context, connectionID uuid.UUID, seq uint64) {
	if !c.refreshWaveActive(waveCtx, seq) {
		return
	}

	c.sessionsMu.RLock()
	sessions := make([]*Session, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	c.sessionsMu.RUnlock()

	var wg sync.WaitGroup
	wg.Add(len(sessions))
	for _, session := range sessions {
		go func(session *Session) {
			defer wg.Done()
			if !c.refreshWaveActive(waveCtx, seq) {
				return
			}
			err := c.refreshSessionConnection(session, waveCtx, connectionID, seq)
			if err == nil || errors.Is(err, net.ErrClosed) || refreshInterrupted(err, session.Context(), waveCtx) {
				return
			}
			if c.refreshSequenceActive(seq) {
				go c.retryRefreshSessionConnection(session, waveCtx, connectionID, seq)
			}
		}(session)
	}
	wg.Wait()
}

// refreshSequenceActive reports whether seq is the currently active refresh
// wave sequence number.
func (c *Client) refreshSequenceActive(seq uint64) bool {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	return c.refreshingSeq == seq
}

// backgroundInstallGate returns a canInstall predicate for a single
// background-work generation.
func (c *Client) backgroundInstallGate(backgroundSeq uint64) func() bool {
	return func() bool {
		return c.backgroundSeq.Load() == backgroundSeq && !c.closing.Load()
	}
}

// handleSubscriptionLossLocked tears down MPSD subscription state while
// holding subscriptionMu. It returns the new background-work generation used
// to invalidate pre-loss retries and scope local session teardown.
func (c *Client) handleSubscriptionLossLocked(subscription *rta.Subscription) uint64 {
	lossSeq := c.backgroundSeq.Add(1)
	c.resetSubscriptionLocked(subscription)
	return lossSeq
}

// logSubscriptionLoss records MPSD subscription loss and shuts down tracked
// sessions after subscription state has been torn down.
func (c *Client) logSubscriptionLoss(msg string, err error, lossSeq uint64) {
	c.log.Error(msg, slog.Any("error", err))
	c.shutdownTrackedSessions(lossSeq)
}

// shutdownTrackedSessions tears down all locally tracked sessions without
// making additional remote calls. It is used when MPSD subscription loss means
// the client can no longer reliably maintain multiplayer session state.
func (c *Client) shutdownTrackedSessions(lossSeq uint64) {
	c.sessionsMu.RLock()
	sessions := make([]*Session, 0, len(c.sessions))
	for _, session := range c.sessions {
		if session.backgroundSeq < lossSeq {
			sessions = append(sessions, session)
		}
	}
	c.sessionsMu.RUnlock()

	for _, session := range sessions {
		session.stopTrackingLocal(lossSeq)
	}
}

// startRefreshWaveIfCurrent starts a refresh wave only if subscription is
// still the active MPSD subscription and backgroundSeq is still current.
// This prevents stale reconnect callbacks from mutating sessions after the
// replacement subscription has already been invalidated.
func (c *Client) startRefreshWaveIfCurrent(subscription *rta.Subscription, backgroundSeq uint64, connectionID uuid.UUID) bool {
	if subscription == nil || c.closing.Load() {
		return false
	}

	c.subscriptionMu.Lock()
	if c.subscription != subscription || c.backgroundSeq.Load() != backgroundSeq || c.closing.Load() {
		c.subscriptionMu.Unlock()
		return false
	}

	c.refreshMu.Lock()
	if c.subscription != subscription || c.backgroundSeq.Load() != backgroundSeq || c.closing.Load() {
		c.refreshMu.Unlock()
		c.subscriptionMu.Unlock()
		return false
	}
	c.cancelRefreshWaveLocked()
	c.refreshSeq++
	seq := c.refreshSeq
	c.refreshingSeq = seq
	waveCtx, cancel := context.WithCancel(context.Background())
	c.refreshCancel = cancel
	c.refreshMu.Unlock()
	c.subscriptionMu.Unlock()

	c.refreshSessionConnections(waveCtx, connectionID, seq)
	return true
}

// refreshContext returns a context with a 15 second timeout that is also
// cancelled when either the session or the wave context is done.
func refreshContext(sessionCtx, waveCtx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	stopSession := context.AfterFunc(sessionCtx, cancel)
	stopWave := context.AfterFunc(waveCtx, cancel)
	return ctx, func() {
		stopSession()
		stopWave()
		cancel()
	}
}

// refreshInterrupted reports whether err is a cancellation caused by either
// the session or wave context being done.
func refreshInterrupted(err error, sessionCtx, waveCtx context.Context) bool {
	return errors.Is(err, context.Canceled) && (sessionCtx.Err() != nil || waveCtx.Err() != nil)
}

// reconnectBackoff returns an exponential backoff duration starting at 200ms
// and capping at 5 seconds.
func reconnectBackoff(attempt int) time.Duration {
	return min(200*time.Millisecond<<attempt, 5*time.Second)
}
