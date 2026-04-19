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
	expected   [operationCapacity]map[uint32]chan<- *handshake
	expectedMu sync.RWMutex

	subscriptions   map[uint32]*Subscription
	subscriptionsMu sync.RWMutex
	// pending holds subscriptions taken out of the live map for a reconnect
	// wave whose individual re-subscribe was interrupted by a replacement
	// socket drop. Because reconnect clears the live subscription map before
	// resubscribing, interrupted subscriptions must be carried into the next
	// reconnect cycle explicitly instead of being retried in-place.
	pending map[*Subscription]struct{}

	log *slog.Logger

	// reconnecting indicates whether the Conn is currently reconnecting to the RTA service.
	reconnecting atomic.Bool
	// reconnectNext indicates the replacement socket dropped before the current
	// reconnect cycle finished, so a new dial should begin immediately after
	// the current resubscribe wave ends.
	reconnectNext atomic.Bool
	// reconnectDone is a channel that is closed when the reconnect is complete.
	// It is nil when no reconnect is in progress.
	reconnectDone chan struct{}
	// reconnectMu guards reconnectDone from concurrent read/write access.
	reconnectMu sync.RWMutex

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
	sub, err := c.subscribe(ctx, resourceURI)
	if err != nil {
		return nil, err
	}
	c.subscriptionsMu.Lock()
	c.subscriptions[sub.id] = sub
	c.subscriptionsMu.Unlock()
	return sub, nil
}

// subscribe performs a sequenced call to subscribe to the given resource URI using
// the provided [context.Context]. The caller is responsible for registering the
// returned [Subscription] in the Conn's subscriptions map using [Subscription.ID].
//
// This is separated from [Conn.Subscribe] because during reconnect, subscriptions
// inherited from previous connection must be re-registered in the map without
// duplicating the subscribe logic.
func (c *Conn) subscribe(ctx context.Context, resourceURI string) (*Subscription, error) {
	h, err := c.call(ctx, operationSubscribe, []any{resourceURI})
	if err != nil {
		return nil, err
	}
	return c.readSubscribeHandshake(resourceURI, h)
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
	switch h.status {
	case StatusOK:
		if len(h.payload) < 2 {
			return nil, &OutOfRangeError{
				Payload: h.payload,
				Index:   1,
			}
		}
		sub := &Subscription{
			resourceURI: resourceURI,

			h: NopSubscriptionHandler{}, // fast-path for defaulting handler without locking
		}
		if err := json.Unmarshal(h.payload[0], &sub.id); err != nil {
			return nil, fmt.Errorf("decode subscription ID: %w", err)
		}
		sub.custom = h.payload[1]
		return sub, nil
	default:
		return nil, unexpectedStatusCode(h.status, h.payload)
	}
}

// Unsubscribe attempts to unsubscribe with a Subscription associated with an ID, with
// the [context.Context] to be used during the handshake. An error may be returned.
func (c *Conn) Unsubscribe(ctx context.Context, sub *Subscription) error {
	h, err := c.call(ctx, operationUnsubscribe, []any{sub.ID()})
	if err != nil {
		return err
	}

	if h.status != StatusOK {
		return unexpectedStatusCode(h.status, h.payload)
	}
	c.subscriptionsMu.Lock()
	delete(c.subscriptions, sub.ID())
	c.subscriptionsMu.Unlock()
	return nil
}

// call sends a sequenced message to the server and blocks using the given
// [context.Context] until the server responds with a matching sequence number.
// The response is then decoded into a [handshake] and returned. The caller is
// responsible for checking its status code.
//
// If the Conn is currently reconnecting, [call] blocks until the reconnect
// completes before sending a message to the server
//
// errReconnectInterrupted is returned by [Conn.callDuringReconnect] when the
// replacement socket drops during a re-subscribe attempt.
var errReconnectInterrupted = errors.New("rta: reconnect interrupted")

// reconnectSettleDelay is the time to wait after resubscribing before
// dispatching reconnect handlers, giving the replacement socket a chance to
// prove it is stable. A short delay (50ms) prioritizes responsiveness over
// stability detection; increase if flaky networks cause spurious reconnects.
const reconnectSettleDelay = 50 * time.Millisecond

func (c *Conn) call(ctx context.Context, op uint8, payload []any) (*handshake, error) {
	for {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}

		seq := c.sequences[op].Add(1)
		ch, err := c.expect(op, seq, payload)
		if err != nil {
			continue
		}
		select {
		case result, ok := <-ch:
			if !ok {
				continue
			}
			c.release(op, seq)
			return result, nil
		case <-ctx.Done():
			c.release(op, seq)
			return nil, ctx.Err()
		case <-c.ctx.Done():
			c.release(op, seq)
			return nil, context.Cause(c.ctx)
		}
	}
}

// callDuringReconnect is [Conn.call] for re-subscribe work that is already
// running inside an active reconnect wave. It must not wait on reconnect
// completion, because the current reconnect wave is blocked on the caller.
// If the replacement socket drops mid-handshake, the caller is told to defer
// the subscription to the next reconnect cycle.
func (c *Conn) callDuringReconnect(ctx context.Context, op uint8, payload []any) (*handshake, error) {
	for {
		seq := c.sequences[op].Add(1)
		ch, err := c.expect(op, seq, payload)
		if err != nil {
			c.reconnectNext.Store(true)
			return nil, errReconnectInterrupted
		}
		select {
		case result, ok := <-ch:
			if !ok {
				c.reconnectNext.Store(true)
				return nil, errReconnectInterrupted
			}
			c.release(op, seq)
			return result, nil
		case <-ctx.Done():
			c.release(op, seq)
			return nil, ctx.Err()
		case <-c.ctx.Done():
			c.release(op, seq)
			return nil, context.Cause(c.ctx)
		}
	}
}

// Subscription represents a subscription contracted with the resource URI available through
// the real-time activity service. A Subscription may be contracted via Conn.Subscribe.
type Subscription struct {
	id          uint32
	custom      json.RawMessage
	resourceURI string

	h  SubscriptionHandler
	mu sync.RWMutex
}

// ID returns the ID assigned to the [Subscription] within a single RTA connection.
func (s *Subscription) ID() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

// Custom returns the custom data associated with the [Subscription].
//
// The format and semantics of this data depend on the resource the
// subscription is targeting. It is received alongside the successful
// subscription response when the [Subscription] was established.
// The custom data may change if the Conn has reconnected to RTA service.
func (s *Subscription) Custom() json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.custom
}

// ResourceURI returns the URI identifying the resource which the [Subscription] is targeting.
// The returned value is identical to the one passed to [Conn.Subscribe].
func (s *Subscription) ResourceURI() string {
	return s.resourceURI
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
	s.mu.Unlock()
}

// handler returns the [SubscriptionHandler] currently registered on the [Subscription].
func (s *Subscription) handler() SubscriptionHandler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.h
}

// SubscriptionHandler is the interface for handling events that may occur in a single
// [Subscription]. An implementation can be registered on a Subscription via [Subscription.Handle].
type SubscriptionHandler interface {
	// HandleEvent handles an event message received over the RTA subscription.
	// The event data reflects what occurred within that subscription.
	// For example, in Social API, an event is received when a user adds or
	// removes the caller.
	HandleEvent(custom json.RawMessage)

	// HandleReconnect is called when the Conn has reconnected to the RTA service
	// and the subscription has been re-established on the new connection.
	//
	// If err is non-nil, the re-subscribe has failed. In this case, the [Subscription]
	// still holds the ID and custom data from the previous connection. The handler does
	// not need to call [Conn.Unsubscribe].
	//
	// If err is nil, the Subscription was successfully re-established on the
	// new connection and has been assigned a new ID. The custom data may also
	// differ from the previous connection depending on the targeting resource.
	// In this case, the handler remains responsible for calling [Conn.Unsubscribe]
	// during cleanup.
	HandleReconnect(err error)
}

// ReconnectReadyHandler is an optional extension interface for subscriptions
// that can safely react as soon as their own re-subscribe has succeeded and the
// replacement socket survives a short stabilization window.
type ReconnectReadyHandler interface {
	HandleReconnectReady()
}

// NopSubscriptionHandler is a no-op implementation of [SubscriptionHandler].
type NopSubscriptionHandler struct{}

func (NopSubscriptionHandler) HandleEvent(json.RawMessage) {}
func (NopSubscriptionHandler) HandleReconnect(error)       {}

// write sends a JSON array composed of the given type and payload over the
// WebSocket connection. A background context is used intentionally, because
// the caller's context must not be passed to WebSocket write methods, as
// cancellation or deadline would close the underlying connection.
func (c *Conn) write(typ uint32, payload []any) error {
	return wsjson.Write(context.Background(), c.currentConn(), append([]any{typ}, payload...))
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

// Wait blocks until any in-progress reconnect attempt has finished.
func (c *Conn) Wait(ctx context.Context) error {
	return c.wait(ctx)
}

// drainExpected closes all pending response channels in c.expected, clears
// the map, and resets all sequence counters to zero so the next sequenced
// call will start from zero again. It is called when the connection is lost.
func (c *Conn) drainExpected() {
	c.expectedMu.Lock()
	for op := range operationCapacity {
		for seq, ch := range c.expected[op] {
			delete(c.expected[op], seq)
			close(ch)
		}
		c.sequences[op].Store(0)
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
	go c.read(conn, done)
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
			c.log.Error("error reading from WebSocket connection", slog.Any("error", err))
			c.triggerReconnect()
			return
		}
		typ, err := readHeader(payload)
		if err != nil {
			c.log.Error("error reading header", slog.Any("error", err))
			continue
		}
		go c.handleMessage(typ, payload[1:])
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
	if c.reconnecting.CompareAndSwap(false, true) {
		done = make(chan struct{})
		c.reconnectDone = done
	} else {
		c.reconnectNext.Store(true)
	}
	c.reconnectMu.Unlock()

	if done != nil {
		go c.reconnect(done)
	}
}

// reconnect re-establishes the WebSocket connection. Only one reconnect may
// run at a time. Concurrent calls after the first are no-ops. If establishment fails,
// the Conn is closed with the error as the cause.
func (c *Conn) reconnect(done chan struct{}) {
	if c.ctx.Err() != nil {
		c.finishReconnect(done)
		return
	}
	defer c.finishReconnect(done)

	c.log.Info("re-establishing WebSocket connection...")

	for {
		if readerDone := c.currentReaderDone(); readerDone != nil {
			select {
			case <-readerDone:
			case <-c.ctx.Done():
				return
			}
		}

		c.reconnectNext.Store(false)

		conn, err := c.dialer.dial(c.ctx)
		if err != nil {
			c.log.Error("error re-establishing WebSocket connection", slog.Any("error", err))
			_ = c.close(fmt.Errorf("rta: reconnect: %w", err))
			return
		}
		c.startReader(conn)

		successes := c.resubscribe()
		if len(successes) != 0 {
			select {
			case <-time.After(reconnectSettleDelay):
			case <-c.ctx.Done():
				return
			}
		}
		if !c.reconnectNext.Load() {
			for _, subscription := range successes {
				go subscription.handler().HandleReconnect(nil)
				c.log.Debug("resubscribed", slog.Group("subscription",
					slog.Uint64("id", uint64(subscription.ID())),
					slog.String("custom", string(subscription.Custom())),
					slog.String("resourceURI", subscription.ResourceURI()),
				))
			}
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
	restart := c.ctx.Err() == nil && c.reconnectNext.Load()
	if restart {
		nextDone = make(chan struct{})
		c.reconnectDone = nextDone
	} else {
		c.reconnecting.Store(false)
	}
	c.reconnectMu.Unlock()

	close(currentDone)

	if restart {
		go c.reconnect(nextDone)
	}
}

// takeSubscriptionsForReconnect collects all subscriptions (active and
// pending) that need to be re-established on the new connection, clears both
// maps, and returns the deduplicated list.
func (c *Conn) takeSubscriptionsForReconnect() []*Subscription {
	c.subscriptionsMu.Lock()
	defer c.subscriptionsMu.Unlock()

	subscriptions := make([]*Subscription, 0, len(c.subscriptions)+len(c.pending))
	seen := make(map[*Subscription]struct{}, len(c.subscriptions)+len(c.pending))
	for _, subscription := range c.subscriptions {
		if _, ok := seen[subscription]; ok {
			continue
		}
		seen[subscription] = struct{}{}
		subscriptions = append(subscriptions, subscription)
	}
	for subscription := range c.pending {
		if _, ok := seen[subscription]; ok {
			continue
		}
		seen[subscription] = struct{}{}
		subscriptions = append(subscriptions, subscription)
	}
	clear(c.subscriptions)
	clear(c.pending)
	return subscriptions
}

// resubscribe re-establishes all subscriptions inherited from the previous
// WebSocket connection. Each re-subscribe attempt has a timeout of 15 seconds.
// Failures are reported via [SubscriptionHandler.HandleReconnect].
func (c *Conn) resubscribe() []*Subscription {
	subscriptions := c.takeSubscriptionsForReconnect()

	c.log.Info("reconnected, resubscribing existing subscriptions...", slog.Int("count", len(subscriptions)))

	successes := make([]*Subscription, 0, len(subscriptions))
	var successesMu sync.Mutex

	wg := new(sync.WaitGroup)
	wg.Add(len(subscriptions))
	for _, s := range subscriptions {
		go func(subscription *Subscription) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(c.ctx, time.Second*15)
			defer cancel()

			sub, err := c.subscribeDuringReconnect(ctx, subscription.ResourceURI())
			if err != nil {
				if errors.Is(err, errReconnectInterrupted) {
					c.subscriptionsMu.Lock()
					if c.pending == nil {
						c.pending = make(map[*Subscription]struct{})
					}
					c.pending[subscription] = struct{}{}
					c.subscriptionsMu.Unlock()
					return
				}
				go subscription.handler().HandleReconnect(err)
				c.log.Error("error resubscribing",
					slog.Group("subscription",
						slog.Uint64("id", uint64(subscription.ID())),
						slog.String("resourceURI", subscription.ResourceURI()),
					),
					slog.Any("error", err),
				)
				return
			}

			subscription.mu.Lock()
			subscription.id = sub.id
			subscription.custom = sub.custom
			subscription.mu.Unlock()

			c.subscriptionsMu.Lock()
			c.subscriptions[subscription.id] = subscription
			c.subscriptionsMu.Unlock()

			successesMu.Lock()
			successes = append(successes, subscription)
			successesMu.Unlock()
			c.scheduleReconnectReady(subscription)
		}(s)
	}

	wg.Wait()
	c.log.Info("resubscribed existing subscriptions", slog.Int("count", len(subscriptions)))
	return successes
}

// scheduleReconnectReady fires [ReconnectReadyHandler.HandleReconnectReady]
// after the settle delay, provided the replacement socket has not been flagged
// for another reconnect.
func (c *Conn) scheduleReconnectReady(subscription *Subscription) {
	handler, ok := subscription.handler().(ReconnectReadyHandler)
	if !ok {
		return
	}
	go func(handler ReconnectReadyHandler) {
		select {
		case <-time.After(reconnectSettleDelay):
		case <-c.ctx.Done():
			return
		}
		if c.reconnectNext.Load() {
			return
		}
		handler.HandleReconnectReady()
	}(handler)
}

// Active reports whether sub is currently registered on this connection.
func (c *Conn) Active(sub *Subscription) bool {
	if sub == nil {
		return false
	}
	c.subscriptionsMu.RLock()
	defer c.subscriptionsMu.RUnlock()
	current, ok := c.subscriptions[sub.ID()]
	return ok && current == sub
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
		hand <- h
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
			go sub.handler().HandleEvent(payload[1])
		}
		c.log.Debug("received event", slog.Group("message", "type", typ, "custom", payload[0]))
	default:
		c.log.Debug("received an unexpected message", slog.Group("message", "type", typ))
	}
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
