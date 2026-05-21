package rta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	reconnectDialTimeout          = 15 * time.Second
	reconnectBackoffInitial       = time.Second
	reconnectBackoffMax           = 30 * time.Second
	proactiveReconnectInterval    = 90 * time.Minute
	resyncSuppressionDuration     = 5 * time.Minute
	maxPendingEventsBeforeHandler = 8
	maxResubscribeBackoff         = 60 * time.Second
)

// Conn represents a connection between the real-time activity services. It can
// be established from Dialer with an authorization token that relies on the
// party 'https://xboxlive.com/'.
//
// A Conn controls subscriptions real-timely under a websocket connection. An
// index-specific JSON array is used for the communication. Conn is safe for
// concurrent use in multiple goroutines.
//
// SubscriptionHandlers are useful to handle any events that may occur in the subscriptions
// controlled by Conn, and can be stored atomically to a Subscription from [Subscription.Handle].
type Conn struct {
	conn   *websocket.Conn
	connMu sync.RWMutex
	// readerDone is closed when the current connection reader exits.
	readerDone chan struct{}

	dialer *dialer

	sequences  [operationCapacity]atomic.Uint32
	expected   [operationCapacity]map[uint32]expectedHandshake
	expectedMu sync.RWMutex
	expectHook func(op uint8, sequence uint32, payload []any) (<-chan *handshake, chan struct{}, error)

	subscriptions   map[uint32]*Subscription
	subscriptionsMu sync.RWMutex

	log *slog.Logger

	// reconnectNext indicates the replacement socket dropped before the current
	// reconnect cycle finished, so a new dial should begin immediately after
	// the current resubscribe wave ends.
	reconnectNext bool
	// reconnectDone is a channel that is closed when the reconnect dial/
	// resubscribe wave is complete. It is nil when no reconnect is in progress.
	reconnectDone chan struct{}
	// reconnectMu guards reconnectDone and reconnectNext.
	reconnectMu sync.RWMutex

	// resyncReadyAt is the time after which RTA resync messages should be
	// delivered. XSAPI suppresses resync immediately after connect/reconnect to
	// avoid a thundering herd of REST refreshes.
	resyncReadyAt time.Time
	resyncMu      sync.RWMutex

	// once ensures that the Conn is closed only once.
	once sync.Once
	// ctx is the background context for the Conn.
	ctx context.Context
	// cancel is a function used to cancel the background ctx of the Conn.
	cancel context.CancelCauseFunc
}

// Subscribe attempts to subscribe with the specific resource URI, with the [context.Context]
// to be used during the handshake. A Subscription may be returned, which contains an ID
// and Custom data as the result of handshake.
func (c *Conn) Subscribe(ctx context.Context, resourceURI string) (*Subscription, error) {
	for {
		sub, readerDone, err := c.subscribe(ctx, resourceURI)
		if err != nil {
			return nil, err
		}
		c.subscriptionsMu.Lock()
		if !c.reconnectWaveStable(readerDone) {
			for id, existing := range c.subscriptions {
				if existing == sub {
					delete(c.subscriptions, id)
				}
			}
			c.subscriptionsMu.Unlock()
			continue
		}
		c.subscriptions[sub.id()] = sub
		c.subscriptionsMu.Unlock()
		return sub, nil
	}
}

// subscribe performs a sequenced call to subscribe to the given resource URI using
// the provided [context.Context]. The caller is responsible for registering the
// returned [Subscription] in the Conn's subscriptions map using [Subscription.ID].
//
// This is separated from [Conn.Subscribe] because during reconnect, subscriptions
// inherited from previous connection must be re-registered in the map without
// duplicating the subscribe logic.
func (c *Conn) subscribe(ctx context.Context, resourceURI string) (*Subscription, chan struct{}, error) {
	sub := &Subscription{resourceURI: resourceURI}
	// The hook applies and registers the successful handshake before the
	// response is released to the waiting caller, so an event sent immediately
	// after the subscribe ACK can be routed. The apply below is intentionally
	// idempotent and exists to return decode/status errors to Subscribe.
	h, readerDone, err := c.callWithHook(ctx, operationSubscribe, func() []any {
		return []any{resourceURI}
	}, true, func(h *handshake) {
		if err := c.applySubscribeHandshake(sub, h); err == nil {
			c.updateSubscriptionID(sub, sub.id())
		}
	})
	if err != nil {
		return nil, nil, err
	}
	if err := c.applySubscribeHandshake(sub, h); err != nil {
		c.removeSubscription(sub)
		return nil, nil, err
	}
	return sub, readerDone, nil
}

// subscribeDuringReconnect re-establishes a subscription while a reconnect
// wave is already in progress. If the replacement socket drops before the
// handshake completes, the call returns [errReconnectInterrupted] so the
// caller can carry the subscription into the next reconnect cycle.
func (c *Conn) subscribeDuringReconnect(ctx context.Context, resourceURI string) (*Subscription, error) {
	h, err := c.callDuringReconnect(ctx, operationSubscribe, []any{resourceURI})
	if err != nil {
		return nil, err
	}
	return c.readSubscribeHandshake(resourceURI, h)
}

// readSubscribeHandshake decodes a successful subscribe handshake into a
// [Subscription].
func (c *Conn) readSubscribeHandshake(resourceURI string, h *handshake) (*Subscription, error) {
	sub := &Subscription{resourceURI: resourceURI}
	if err := c.applySubscribeHandshake(sub, h); err != nil {
		return nil, err
	}
	return sub, nil
}

// applySubscribeHandshake applies a successful subscribe handshake to sub.
func (c *Conn) applySubscribeHandshake(sub *Subscription, h *handshake) error {
	switch h.status {
	case StatusOK:
		if len(h.payload) < 2 {
			return &OutOfRangeError{
				Payload: h.payload,
				Index:   1,
			}
		}
		var id uint32
		if err := json.Unmarshal(h.payload[0], &id); err != nil {
			return fmt.Errorf("decode subscription ID: %w", err)
		}
		sub.mu.Lock()
		if sub.ID == 0 {
			sub.ID = id
			sub.Custom = append(json.RawMessage(nil), h.payload[1]...)
		}
		sub.currentID = id
		sub.currentCustom = append(json.RawMessage(nil), h.payload[1]...)
		sub.currentSet = true
		sub.mu.Unlock()
		return nil
	default:
		return unexpectedStatusCode(h.status, h.payload)
	}
}

// Unsubscribe attempts to unsubscribe with a Subscription associated with an ID, with
// the [context.Context] to be used during the handshake. An error may be returned.
func (c *Conn) Unsubscribe(ctx context.Context, sub *Subscription) error {
	var id uint32
	h, _, err := c.callWithPayload(ctx, operationUnsubscribe, func() []any {
		id = sub.id()
		return []any{id}
	})
	if err != nil {
		return err
	}

	if h.status != StatusOK {
		return unexpectedStatusCode(h.status, h.payload)
	}
	c.removeSubscription(sub)
	return nil
}

var errReconnectInterrupted = errors.New("rta: reconnect interrupted")

// callWithPayload sends a sequenced message to the server and blocks using the
// given [context.Context] until the server responds with a matching sequence
// number. If the Conn is reconnecting, callWithPayload blocks until reconnect
// completes before sending.
func (c *Conn) callWithPayload(ctx context.Context, op uint8, payload func() []any) (*handshake, chan struct{}, error) {
	return c.call(ctx, op, payload, true)
}

// call sends one RTA request and waits for its sequenced response. When wait
// is true, it first waits for any reconnect wave to finish; reconnect-internal
// resubscribe calls pass wait=false so they do not wait on themselves.
func (c *Conn) call(ctx context.Context, op uint8, payload func() []any, wait bool) (*handshake, chan struct{}, error) {
	return c.callWithHook(ctx, op, payload, wait, nil)
}

func (c *Conn) callWithHook(ctx context.Context, op uint8, payload func() []any, wait bool, beforeDeliver func(*handshake)) (*handshake, chan struct{}, error) {
	for {
		if wait {
			if err := c.wait(ctx); err != nil {
				return nil, nil, err
			}
		}

		seq := c.sequences[op].Add(1)
		ch, readerDone, err := c.expectWithHook(op, seq, payload(), beforeDeliver)
		if err != nil {
			if !wait {
				return nil, nil, errReconnectInterrupted
			}
			continue
		}
		select {
		case result, ok := <-ch:
			if !ok {
				if !wait {
					c.release(op, seq)
					return nil, nil, errReconnectInterrupted
				}
				continue
			}
			c.release(op, seq)
			return result, readerDone, nil
		case <-ctx.Done():
			c.release(op, seq)
			select {
			case result, ok := <-ch:
				if ok {
					return result, readerDone, nil
				}
			default:
			}
			return nil, nil, ctx.Err()
		case <-c.ctx.Done():
			c.release(op, seq)
			select {
			case result, ok := <-ch:
				if ok {
					return result, readerDone, nil
				}
			default:
			}
			return nil, nil, context.Cause(c.ctx)
		}
	}
}

// callDuringReconnect is [Conn.call] for re-subscribe work that is already
// running inside an active reconnect wave. It must not wait on reconnect
// completion, because the current reconnect wave is blocked on the caller.
// If the replacement socket drops mid-handshake, the caller is told to defer
// the subscription to the next reconnect cycle.
func (c *Conn) callDuringReconnect(ctx context.Context, op uint8, payload []any) (*handshake, error) {
	h, _, err := c.call(ctx, op, func() []any {
		return payload
	}, false)
	if errors.Is(err, errReconnectInterrupted) {
		c.markReconnectNext()
	}
	return h, err
}

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
	resourceURI   string

	h    SubscriptionHandler
	hSet bool
	mu   sync.RWMutex
	// pending holds events delivered after the subscription is registered but
	// before the caller installs a handler on it.
	pending []json.RawMessage
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

// setCurrent records the active service-side ID and custom payload.
func (s *Subscription) setCurrent(id uint32, custom json.RawMessage) {
	s.mu.Lock()
	s.currentID = id
	s.currentCustom = custom
	s.currentSet = true
	s.mu.Unlock()
}

// custom returns the active custom payload. It may differ from the exported
// Custom field after RTA reconnects and resubscribes.
func (s *Subscription) custom() json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.currentSet {
		return s.currentCustom
	}
	return s.Custom
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
	s.mu.Unlock()
	s.dispatchPending(pending)
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
	s.mu.Lock()
	if !s.hSet {
		if len(s.pending) < maxPendingEventsBeforeHandler {
			s.pending = append(s.pending, append(json.RawMessage(nil), custom...))
		}
		s.mu.Unlock()
		return
	}
	h := s.h
	s.mu.Unlock()
	h.HandleEvent(custom)
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

// write sends a JSON array composed of the given type and payload over the
// current WebSocket connection and returns the reader lifecycle channel for
// the connection used by the write. A background context is used intentionally,
// because the caller's context must not be passed to WebSocket write methods,
// as cancellation or deadline would close the underlying connection.
func (c *Conn) write(typ uint32, payload []any) (chan struct{}, error) {
	c.connMu.RLock()
	conn := c.conn
	readerDone := c.readerDone
	c.connMu.RUnlock()
	return readerDone, wsjson.Write(context.Background(), conn, append([]any{typ}, payload...))
}

// wait blocks until any in-progress reconnect attempt has finished.
func (c *Conn) wait(ctx context.Context) error {
	for {
		c.reconnectMu.RLock()
		done := c.reconnectDone
		c.reconnectMu.RUnlock()

		if done == nil {
			if err := context.Cause(c.ctx); err != nil {
				return err
			}
			return nil
		}
		select {
		case <-done:
			if err := context.Cause(c.ctx); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-c.ctx.Done():
			return context.Cause(c.ctx)
		}
	}
}

// drainExpected closes all pending response channels in c.expected and clears
// the map when the connection is lost. Sequence counters are intentionally not
// reset: callers blocked on the old connection may still call release after
// this returns, and reusing their sequence numbers could let those stale
// releases delete response channels for new calls on the replacement socket.
func (c *Conn) drainExpected() {
	c.expectedMu.Lock()
	for op := range operationCapacity {
		for seq, ch := range c.expected[op] {
			delete(c.expected[op], seq)
			close(ch.response)
		}
	}
	c.expectedMu.Unlock()
}

// startReader installs conn as the active connection and spawns a goroutine
// to read messages from it.
func (c *Conn) startReader(conn *websocket.Conn) {
	done := make(chan struct{})
	c.connMu.Lock()
	c.conn = conn
	c.readerDone = done
	c.connMu.Unlock()
	c.suppressResyncFor(resyncSuppressionDuration)
	go c.read(conn, done)
	go c.refreshConnAfter(conn, done, proactiveReconnectInterval)
}

// currentConn returns the active WebSocket connection.
func (c *Conn) currentConn() *websocket.Conn {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.conn
}

// currentReaderDone returns the channel that is closed when the active
// reader goroutine exits.
func (c *Conn) currentReaderDone() chan struct{} {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.readerDone
}

// isCurrentConn reports whether conn is the active WebSocket connection.
func (c *Conn) isCurrentConn(conn *websocket.Conn) bool {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.conn == conn
}

// refreshConnAfter proactively closes conn after the configured RTA lifetime
// window, causing the reader to drive the normal reconnect/resubscribe path.
func (c *Conn) refreshConnAfter(conn *websocket.Conn, readerDone chan struct{}, after time.Duration) {
	timer := time.NewTimer(after)
	defer timer.Stop()

	select {
	case <-timer.C:
		if c.currentReaderDone() == readerDone && c.ctx.Err() == nil {
			_ = conn.Close(websocket.StatusNormalClosure, "refresh RTA token")
		}
	case <-readerDone:
	case <-c.ctx.Done():
	}
}

// read continuously reads JSON messages from the WebSocket connection and
// dispatches them for handling. If the connection is lost unexpectedly, it
// triggers a reconnect. If the Conn was closed by the user via [Conn.Close],
// no reconnect is attempted.
func (c *Conn) read(conn *websocket.Conn, done chan struct{}) {
	defer close(done)
	defer func() {
		if c.isCurrentConn(conn) {
			c.drainExpected()
		}
	}()

	for {
		var payload []json.RawMessage
		if err := wsjson.Read(context.Background(), conn, &payload); err != nil {
			if c.ctx.Err() != nil {
				// Conn was closed by the user. Do not reconnect.
				return
			}
			if !c.isCurrentConn(conn) {
				return
			}
			var closeErr websocket.CloseError
			if errors.As(err, &closeErr) && closeErr.Code == websocket.StatusNormalClosure {
				c.log.Debug("WebSocket connection closed normally", slog.Any("error", err))
			} else {
				c.log.Error("error reading from WebSocket connection", slog.Any("error", err))
			}
			c.triggerReconnect()
			return
		}
		typ, err := readHeader(payload)
		if err != nil {
			c.log.Error("error reading header", slog.Any("error", err))
			continue
		}
		c.handleMessage(typ, payload[1:])
	}
}

// triggerReconnect starts a reconnect if none is running, or signals the
// running reconnect to retry with a fresh dial once its current wave ends.
func (c *Conn) triggerReconnect() {
	if c.ctx.Err() != nil {
		return
	}
	var done chan struct{}
	c.reconnectMu.Lock()
	if c.reconnectDone == nil {
		done = make(chan struct{})
		c.reconnectDone = done
	} else {
		c.reconnectNext = true
	}
	c.reconnectMu.Unlock()

	if done != nil {
		go c.reconnect(done)
	}
}

// reconnect re-establishes the WebSocket connection. Only one reconnect may
// run at a time.
func (c *Conn) reconnect(done chan struct{}) {
	if c.ctx.Err() != nil {
		c.finishReconnect(done)
		return
	}
	defer c.finishReconnect(done)

	c.log.Info("re-establishing WebSocket connection...")
	backoff := reconnectBackoffInitial

	for {
		if readerDone := c.currentReaderDone(); readerDone != nil {
			select {
			case <-readerDone:
			case <-c.ctx.Done():
				return
			}
		}

		c.reconnectMu.Lock()
		c.reconnectNext = false
		c.reconnectMu.Unlock()

		dialCtx, cancel := context.WithTimeout(c.ctx, reconnectDialTimeout)
		conn, err := c.dialer.dial(dialCtx)
		cancel()
		if err != nil {
			c.log.Error("error re-establishing WebSocket connection",
				slog.Any("error", err),
				slog.Duration("retry_after", backoff),
			)
			select {
			case <-time.After(backoff):
				backoff = minDuration(backoff*2, reconnectBackoffMax)
				continue
			case <-c.ctx.Done():
				return
			}
		}
		backoff = reconnectBackoffInitial
		c.startReader(conn)

		successes := c.resubscribe()
		readerDone := c.currentReaderDone()
		for _, subscription := range successes {
			go c.notifyReconnect(subscription, nil)
			c.log.Debug("resubscribed", slog.Group("subscription",
				slog.Uint64("id", uint64(subscription.id())),
				slog.String("custom", string(subscription.custom())),
				slog.String("resourceURI", subscription.resourceURI),
			))
		}
		if c.reconnectWaveStable(readerDone) {
			return
		}
	}
}

// finishReconnect closes done to unblock waiters and, if reconnectNext was
// set during the cycle, starts a new reconnect immediately.
func (c *Conn) finishReconnect(done chan struct{}) {
	currentDone := done
	var nextDone chan struct{}
	c.reconnectMu.Lock()
	if c.reconnectDone == currentDone {
		c.reconnectDone = nil
	}
	restart := c.ctx.Err() == nil && c.reconnectNext
	if restart {
		c.reconnectNext = false
		nextDone = make(chan struct{})
		c.reconnectDone = nextDone
	}
	c.reconnectMu.Unlock()

	close(currentDone)

	if restart {
		go c.reconnect(nextDone)
	}
}

// subscriptionsForReconnect collects the deduplicated set of subscriptions
// that need to be re-established on the current connection.
func (c *Conn) subscriptionsForReconnect() []*Subscription {
	c.subscriptionsMu.RLock()
	defer c.subscriptionsMu.RUnlock()

	subscriptions := make([]*Subscription, 0, len(c.subscriptions))
	seen := make(map[*Subscription]struct{}, len(c.subscriptions))
	for _, subscription := range c.subscriptions {
		if _, ok := seen[subscription]; ok {
			continue
		}
		seen[subscription] = struct{}{}
		subscriptions = append(subscriptions, subscription)
	}
	return subscriptions
}

// updateSubscriptionID replaces any stale routing entry for subscription with
// id from the latest successful subscribe handshake.
func (c *Conn) updateSubscriptionID(subscription *Subscription, id uint32) {
	c.subscriptionsMu.Lock()
	defer c.subscriptionsMu.Unlock()
	for existingID, existingSubscription := range c.subscriptions {
		if existingSubscription == subscription {
			delete(c.subscriptions, existingID)
		}
	}
	c.subscriptions[id] = subscription
}

// removeSubscription removes every routing entry pointing to subscription.
func (c *Conn) removeSubscription(subscription *Subscription) {
	c.subscriptionsMu.Lock()
	defer c.subscriptionsMu.Unlock()
	for id, existingSubscription := range c.subscriptions {
		if existingSubscription == subscription {
			delete(c.subscriptions, id)
		}
	}
}

// resubscribe re-establishes all subscriptions inherited from the previous
// WebSocket connection. Each re-subscribe attempt has a timeout of 15 seconds.
// Failures are reported via [SubscriptionHandler.HandleReconnect].
func (c *Conn) resubscribe() []*Subscription {
	subscriptions := c.subscriptionsForReconnect()

	c.log.Info("reconnected, resubscribing existing subscriptions...", slog.Int("count", len(subscriptions)))

	successes := make([]*Subscription, 0, len(subscriptions))
	var successesMu sync.Mutex

	wg := new(sync.WaitGroup)
	wg.Add(len(subscriptions))
	for _, s := range subscriptions {
		go func(subscription *Subscription) {
			defer wg.Done()

			attempt := 0
			for {
				ctx, cancel := context.WithTimeout(c.ctx, time.Second*15)
				sub, err := c.subscribeDuringReconnect(ctx, subscription.resourceURI)
				cancel()
				if err == nil {
					subscriptionID := sub.id()
					subscription.setCurrent(subscriptionID, sub.custom())
					c.updateSubscriptionID(subscription, subscriptionID)

					successesMu.Lock()
					successes = append(successes, subscription)
					successesMu.Unlock()
					return
				}
				if errors.Is(err, errReconnectInterrupted) {
					return
				}
				if retryableSubscribeError(err) {
					delay := resubscribeBackoff(attempt)
					attempt++
					c.log.Warn("retrying RTA resubscribe after transient status",
						slog.Group("subscription",
							slog.Uint64("id", uint64(subscription.id())),
							slog.String("resourceURI", subscription.resourceURI),
						),
						slog.Any("error", err),
						slog.Duration("retry_after", delay),
					)
					if delay <= 0 {
						continue
					}
					select {
					case <-time.After(delay):
						continue
					case <-c.ctx.Done():
						return
					}
				}

				c.removeSubscription(subscription)
				go func(subscription *Subscription, err error) {
					c.notifyReconnect(subscription, err)
				}(subscription, err)
				c.log.Error("error resubscribing",
					slog.Group("subscription",
						slog.Uint64("id", uint64(subscription.id())),
						slog.String("resourceURI", subscription.resourceURI),
					),
					slog.Any("error", err),
				)
				return
			}
		}(s)
	}

	wg.Wait()
	c.log.Info("resubscribed existing subscriptions",
		slog.Int("attempted", len(subscriptions)),
		slog.Int("successful", len(successes)),
	)
	return successes
}

// retryableSubscribeError reports whether err is an RTA subscribe status that
// XSAPI treats as transient during resubscribe.
func retryableSubscribeError(err error) bool {
	var status *UnexpectedStatusError
	if !errors.As(err, &status) {
		return false
	}
	switch status.Code {
	case StatusThrottled, StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

// resubscribeBackoff returns XSAPI-style quadratic backoff capped at 60s.
func resubscribeBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Duration(attempt*attempt) * time.Second
	if delay > maxResubscribeBackoff {
		return maxResubscribeBackoff
	}
	return delay
}

// reconnectWaveStable reports whether the reconnect wave still points at the
// same live reader channel and has not already been marked for another retry.
func (c *Conn) reconnectWaveStable(readerDone chan struct{}) bool {
	c.reconnectMu.RLock()
	next := c.reconnectNext
	c.reconnectMu.RUnlock()
	if next {
		return false
	}
	if readerDone == nil {
		return true
	}
	if c.currentReaderDone() != readerDone {
		return false
	}
	select {
	case <-readerDone:
		return false
	default:
		return true
	}
}

// markReconnectNext asks the active reconnect wave to retry with a fresh dial.
func (c *Conn) markReconnectNext() {
	c.reconnectMu.Lock()
	c.reconnectNext = true
	c.reconnectMu.Unlock()
}

func (c *Conn) notifyReconnect(subscription *Subscription, err error) {
	handler, ok := subscription.handler().(ReconnectHandler)
	if !ok {
		return
	}
	handler.HandleReconnect(err)
}

// Close closes the websocket connection with websocket.StatusNormalClosure.
func (c *Conn) Close() (err error) {
	return c.close(net.ErrClosed)
}

// close closes the WebSocket connection with [websocket.StatusNormalClosure],
// then cancels the background context of the Conn with the given reason so that
// any blocking methods in [Conn] can return it from [context.Cause].
func (c *Conn) close(cause error) (err error) {
	c.once.Do(func() {
		c.cancel(cause)
		err = c.currentConn().Close(websocket.StatusNormalClosure, "")
	})
	return err
}

// handleMessage handles a message received in read with the type.
func (c *Conn) handleMessage(typ uint32, payload []json.RawMessage) {
	switch typ {
	case typeSubscribe, typeUnsubscribe: // Subscribe & Unsubscribe handshake response
		h, err := readHandshake(payload)
		if err != nil {
			c.log.Error("error reading handshake response", slog.Any("error", err))
			return
		}
		op := typeToOperation(typ)
		c.expectedMu.Lock()
		defer c.expectedMu.Unlock()
		hand, ok := c.expected[op][h.sequence]
		if !ok {
			c.log.Debug("unexpected handshake response", slog.Group("message", "type", typ, "sequence", h.sequence))
			return
		}
		if hand.beforeDeliver != nil {
			hand.beforeDeliver(h)
		}
		hand.response <- h
	case typeEvent:
		if len(payload) < 2 {
			c.log.Debug("event message has no custom")
			return
		}
		var subscriptionID uint32
		if err := json.Unmarshal(payload[0], &subscriptionID); err != nil {
			c.log.Error("error decoding subscription ID", slog.Any("error", err))
			return
		}
		c.subscriptionsMu.RLock()
		sub, ok := c.subscriptions[subscriptionID]
		c.subscriptionsMu.RUnlock()
		if ok {
			sub.dispatchEvent(payload[1])
		}
		c.log.Debug("received event", slog.Group("message", "type", typ, "custom", payload[0]))
	case typeResync:
		c.notifyResync()
	default:
		c.log.Debug("received an unexpected message", slog.Group("message", "type", typ))
	}
}

// dispatchPending delivers events that arrived after subscription registration
// but before the caller installed a handler.
func (s *Subscription) dispatchPending(events []json.RawMessage) {
	for _, event := range events {
		s.dispatchEvent(event)
	}
}

// notifyResync delivers an RTA resync signal to subscriptions that know how to
// refresh their backing resource.
func (c *Conn) notifyResync() {
	if !c.resyncReady() {
		c.log.Debug("ignored RTA resync during post-connect suppression window")
		return
	}

	for _, subscription := range c.subscriptionsForReconnect() {
		handler, ok := subscription.handler().(ResyncHandler)
		if ok {
			go handler.HandleResync()
		}
	}
}

// suppressResyncFor suppresses RTA resync delivery until d has elapsed.
func (c *Conn) suppressResyncFor(d time.Duration) {
	c.resyncMu.Lock()
	c.resyncReadyAt = time.Now().Add(d)
	c.resyncMu.Unlock()
}

// resyncReady reports whether post-connect resync suppression has elapsed.
func (c *Conn) resyncReady() bool {
	c.resyncMu.RLock()
	readyAt := c.resyncReadyAt
	c.resyncMu.RUnlock()
	return readyAt.IsZero() || time.Now().After(readyAt)
}

// minDuration returns the smaller of a and b.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// An OutOfRangeError occurs when reading values from payload received from the service.
// The Payload specifies the remaining values included in the payload, and the Index specifies
// a length of values that is missing from the payload.
type OutOfRangeError struct {
	Payload []json.RawMessage
	Index   int
}

// Error returns a string representation of the Error in the same format
// used by [runtime.boundsError].
func (e *OutOfRangeError) Error() string {
	return fmt.Sprintf("xsapi/rta: index out of range [%d] with length %d", e.Index, len(e.Payload))
}

// readHeader decodes a header from the first 1 value from the payload. An OutOfRangeError
// may be returned if the payload has not enough length to read.
func readHeader(payload []json.RawMessage) (typ uint32, err error) {
	if len(payload) < 1 {
		return typ, &OutOfRangeError{
			Payload: payload,
			Index:   0,
		}
	}
	return typ, json.Unmarshal(payload[0], &typ)
}

// readHandshake decodes a handshake from the first 2 values from the payload.
// An OutOfRangeError may be returned if the payload has not enough length to read.
func readHandshake(payload []json.RawMessage) (*handshake, error) {
	if len(payload) < 2 {
		return nil, &OutOfRangeError{
			Payload: payload,
			Index:   2,
		}
	}
	h := &handshake{}
	if err := json.Unmarshal(payload[0], &h.sequence); err != nil {
		return nil, fmt.Errorf("decode sequence: %w", err)
	}
	if err := json.Unmarshal(payload[1], &h.status); err != nil {
		return nil, fmt.Errorf("decode status code: %w", err)
	}
	h.payload = payload[2:]
	return h, nil
}

// unexpectedStatusCode wraps an UnexpectedStatusError from the status.
// If the payload has more than one remaining values, it will try to decode
// them as an error message.
func unexpectedStatusCode(status int32, payload []json.RawMessage) error {
	err := &UnexpectedStatusError{Code: status}
	if len(payload) >= 1 {
		_ = json.Unmarshal(payload[0], &err.Message)
	}
	return err
}
