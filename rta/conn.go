package rta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
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

	dialer *dialer

	sequences  [operationCapacity]atomic.Uint32
	expected   [operationCapacity]map[uint32]expectedCall
	expectedMu sync.Mutex

	subscriptions   map[uint32]*Subscription
	subscriptionsMu sync.RWMutex

	log *slog.Logger

	// reconnecting indicates whether the Conn is currently reconnecting to the RTA service.
	reconnecting atomic.Bool
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

// Subscribe attempts to subscribe to the specific resource URI, with the
// [context.Context] to be used during the handshake. A Subscription may be
// returned, which contains an ID and Custom data as the result of handshake.
func (c *Conn) Subscribe(ctx context.Context, resourceURI string) (*Subscription, error) {
	sub := NewSubscription(resourceURI, nil)
	if err := c.SubscribeWith(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

// SubscribeWith attempts to subscribe using a caller-owned Subscription. It is
// useful for services that need to preserve the same subscription object across
// reconnects.
func (c *Conn) SubscribeWith(ctx context.Context, sub *Subscription) error {
	if sub == nil {
		return errors.New("rta: nil subscription")
	}
	for {
		if err := c.wait(ctx); err != nil {
			return err
		}
		sub.opMu.Lock()
		if sub.Active() {
			sub.opMu.Unlock()
			return nil
		}
		err := c.subscribe(ctx, sub)
		sub.opMu.Unlock()
		if err == errConnectionInterrupted {
			if err := pauseAfterConnectionInterrupt(ctx, c.ctx); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			sub.deactivate(err)
			return err
		}
		c.trackSubscription(sub)
		return nil
	}
}

// subscribe performs a sequenced call to subscribe to the given resource URI using
// the provided [context.Context]. The caller is responsible for registering the
// returned [Subscription] in the Conn's subscriptions map using [Subscription.ID].
//
// This is separated from [Conn.Subscribe] because during reconnect, subscriptions
// inherited from previous connection must be re-registered in the map without
// duplicating the subscribe logic.
func (c *Conn) subscribe(ctx context.Context, sub *Subscription) error {
	h, err := c.call(ctx, operationSubscribe, []any{sub.ResourceURI()})
	if err != nil {
		return err
	}

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
		custom := slices.Clone(h.payload[1])
		sub.activate(id, custom)
		if err := sub.handleSubscribe(custom); err != nil {
			// This resource has failed to understand this subscription.
			if err2 := c.unsubscribeID(ctx, id); err2 != nil {
				err = errors.Join(err, fmt.Errorf("unsubscribe: %w", err2))
			}
			return err
		}
		return nil
	default:
		return unexpectedStatusCode(h.status, h.payload)
	}
}

// Unsubscribe attempts to unsubscribe with a Subscription associated with an ID, with
// the [context.Context] to be used during the handshake. An error may be returned.
func (c *Conn) Unsubscribe(ctx context.Context, sub *Subscription) error {
	if sub == nil {
		return errors.New("rta: nil subscription")
	}
	if err := c.wait(ctx); err != nil {
		return err
	}
	sub.opMu.Lock()
	if !sub.Active() {
		sub.opMu.Unlock()
		return nil
	}
	sub.setUnsubscribing(true)
	err := c.unsubscribeID(ctx, sub.ID())
	if err != nil && err != errConnectionInterrupted {
		sub.setUnsubscribing(false)
		sub.opMu.Unlock()
		return err
	}
	c.untrackSubscription(sub)
	// Notify that the subscription has been unsubscribed so the service
	// might be able to clean up resources tied to this subscription.
	sub.deactivate(ErrUnsubscribed)
	sub.opMu.Unlock()
	return nil
}

// ErrUnsubscribed is an error notified by [SubscriptionHandler.HandleError] when
// the RTA subscription is unsubscribed by the user.
var ErrUnsubscribed = errors.New("rta: subscription removed from RTA connection")

func (c *Conn) unsubscribeID(ctx context.Context, id uint32) error {
	h, err := c.call(ctx, operationUnsubscribe, []any{id})
	if err != nil {
		return err
	}
	if h.status != StatusOK {
		return unexpectedStatusCode(h.status, h.payload)
	}
	return nil
}

// call sends a sequenced message to the server and blocks using the given
// [context.Context] until the server responds with a matching sequence number.
// The response is then decoded into a response and returned. The caller is
// responsible for checking its status code.
//
// Callers that run outside the reconnect path should call [Conn.wait] before
// calling call.
func (c *Conn) call(ctx context.Context, op uint8, payload []any) (*response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.ctx.Err(); err != nil {
		return nil, context.Cause(c.ctx)
	}

	seq := c.sequences[op].Add(1)
	conn := c.currentConn()
	ch := c.expect(conn, op, seq)
	if err := c.write(conn, operationToType(op), append([]any{seq}, payload...)); err != nil {
		c.release(op, seq)
		c.drainExpected(conn)
		go c.reconnect()
		return nil, errConnectionInterrupted
	}
	select {
	case result, ok := <-ch:
		if !ok {
			return nil, errConnectionInterrupted
		}
		return result, nil
	case <-ctx.Done():
		c.release(op, seq)
		return nil, ctx.Err()
	case <-c.ctx.Done():
		c.release(op, seq)
		return nil, context.Cause(c.ctx)
	}
}

// errConnectionInterrupted marks a call that lost its socket while the parent
// connection is still allowed to reconnect and retry the operation.
var errConnectionInterrupted = errors.New("rta: connection interrupted")

// connectionInterruptRetryDelay briefly yields after a socket interruption so
// the reconnect goroutine can publish reconnect state before callers retry.
const connectionInterruptRetryDelay = 10 * time.Millisecond

// pauseAfterConnectionInterrupt waits before retrying an interrupted call, while
// still respecting both the caller context and the connection lifetime context.
func pauseAfterConnectionInterrupt(ctx, connCtx context.Context) error {
	select {
	case <-time.After(connectionInterruptRetryDelay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-connCtx.Done():
		return context.Cause(connCtx)
	}
}

func NewSubscription(resourceURI string, h SubscriptionHandler) *Subscription {
	sub := &Subscription{resourceURI: resourceURI}
	sub.Handle(h)
	return sub
}

// Subscription represents a subscription contracted with the resource URI available through
// the real-time activity service. A Subscription may be contracted via Conn.Subscribe.
type Subscription struct {
	id     uint32
	custom json.RawMessage

	h           atomic.Pointer[SubscriptionHandler]
	mu          sync.RWMutex
	opMu        sync.Mutex
	resourceURI string

	// active indicates whether the Subscription is currently active on the RTA connection.
	active        bool
	unsubscribing bool
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
	return slices.Clone(s.custom)
}

// ResourceURI returns the URI identifying the resource which the [Subscription] is targeting.
// The returned value is identical to the one passed to [Conn.Subscribe].
func (s *Subscription) ResourceURI() string {
	return s.resourceURI
}

// Active reports whether the [Subscription] is currently active.
func (s *Subscription) Active() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

func (s *Subscription) shouldResubscribe() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active && !s.unsubscribing
}

func (s *Subscription) setUnsubscribing(unsubscribing bool) {
	s.mu.Lock()
	s.unsubscribing = unsubscribing
	s.mu.Unlock()
}

func (s *Subscription) activate(id uint32, custom json.RawMessage) {
	s.mu.Lock()
	s.id, s.custom = id, slices.Clone(custom)
	s.active, s.unsubscribing = true, false
	s.mu.Unlock()
}

// deactivate deactivates the Subscription using the given error as the cause.
// When err is non-nil, it is reported to the resource via [SubscriptionHandler.HandleError].
// When the Subscription is already inactive, deactivate is no-op.
func (s *Subscription) deactivate(cause error) {
	s.mu.Lock()
	active := s.active
	s.active, s.unsubscribing = false, false
	s.mu.Unlock()
	if active && cause != nil {
		s.handleError(cause)
	}
}

// Handle registers a [SubscriptionHandler] on the [Subscription] to handle
// future events that may occur in the subscription. If h is nil, a no-op
// handler is registered.
func (s *Subscription) Handle(h SubscriptionHandler) {
	if h == nil {
		h = NopSubscriptionHandler{}
	}
	s.h.Store(&h)
}

// handler returns the [SubscriptionHandler] currently registered on the [Subscription].
func (s *Subscription) handler() SubscriptionHandler {
	if h := s.h.Load(); h != nil {
		return *h
	}
	return NopSubscriptionHandler{}
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

// SubscribeHandler is an optional extension interface for handlers that need
// to process the custom payload returned by a successful subscribe handshake.
type SubscribeHandler interface {
	HandleSubscribe(custom json.RawMessage) error
}

// ResyncHandler is an optional extension interface for handlers that need to
// react to RTA resync messages.
type ResyncHandler interface {
	// HandleResync is called when a Resync message is received from the RTA service
	// and the resource targeted by the Subscription may have been changed.
	HandleResync()
}

// ErrorHandler is an optional extension interface for handlers that need to
// observe unrecoverable subscription errors.
type ErrorHandler interface {
	// HandleError is called when an unrecoverable error has occurred for this subscription.
	// The caller may need to resubscribe in order to receive updates for the resource.
	HandleError(err error)
}

// NopSubscriptionHandler is a no-op implementation of [SubscriptionHandler].
type NopSubscriptionHandler struct{}

func (NopSubscriptionHandler) HandleSubscribe(json.RawMessage) error { return nil }
func (NopSubscriptionHandler) HandleEvent(json.RawMessage)           {}
func (NopSubscriptionHandler) HandleResync()                         {}
func (NopSubscriptionHandler) HandleError(error)                     {}

func (s *Subscription) handleSubscribe(custom json.RawMessage) error {
	if h, ok := s.handler().(SubscribeHandler); ok {
		return h.HandleSubscribe(custom)
	}
	return nil
}

func (s *Subscription) handleEvent(custom json.RawMessage) {
	s.handler().HandleEvent(custom)
}

func (s *Subscription) handleResync() {
	if h, ok := s.handler().(ResyncHandler); ok {
		h.HandleResync()
	}
}

func (s *Subscription) handleError(err error) {
	if h, ok := s.handler().(ErrorHandler); ok {
		go h.HandleError(err)
	}
}

// write sends a JSON array composed of the given type and payload over the
// WebSocket connection. A background context is used intentionally, because
// the caller's context must not be passed to WebSocket write methods, as
// cancellation or deadline would close the underlying connection.
func (c *Conn) write(conn *websocket.Conn, typ uint32, payload []any) error {
	return wsjson.Write(context.Background(), conn, append([]any{typ}, payload...))
}

// wait blocks until any in-progress reconnect attempt has finished.
func (c *Conn) wait(ctx context.Context) error {
	c.reconnectMu.RLock()
	done := c.reconnectDone
	c.reconnectMu.RUnlock()

	if done == nil {
		return nil
	}
	select {
	case <-done:
		return context.Cause(c.ctx) // nil unless the Conn was closed
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return context.Cause(c.ctx)
	}
}

// drainExpected closes pending response channels tied to conn. It is called
// when a WebSocket connection is lost.
func (c *Conn) drainExpected(conn *websocket.Conn) {
	c.expectedMu.Lock()
	for op := range operationCapacity {
		for seq, expected := range c.expected[op] {
			if expected.conn != conn {
				continue
			}
			delete(c.expected[op], seq)
			close(expected.ch)
		}
	}
	c.expectedMu.Unlock()
}

func (c *Conn) trackSubscription(sub *Subscription) {
	c.subscriptionsMu.Lock()
	c.subscriptions[sub.ID()] = sub
	c.subscriptionsMu.Unlock()
}

func (c *Conn) untrackSubscription(sub *Subscription) {
	c.subscriptionsMu.Lock()
	delete(c.subscriptions, sub.ID())
	c.subscriptionsMu.Unlock()
}

// currentConn returns the currently-active WebSocket connection.
// It is safe for concurrent use.
func (c *Conn) currentConn() *websocket.Conn {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.conn
}

// isCurrentConn reports whether conn is still the active WebSocket connection.
func (c *Conn) isCurrentConn(conn *websocket.Conn) bool {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.conn == conn
}

// read continuously reads JSON messages from the WebSocket connection and
// dispatches them for handling. If the connection is lost unexpectedly, it
// triggers a reconnect. If the Conn was closed by the user via [Conn.Close],
// no reconnect is attempted.
func (c *Conn) read(conn *websocket.Conn) {
	for {
		var payload []json.RawMessage
		if err := wsjson.Read(context.Background(), conn, &payload); err != nil {
			c.drainExpected(conn)
			if !c.isCurrentConn(conn) {
				return
			}
			if c.ctx.Err() != nil {
				// Conn was closed by the user. Do not reconnect.
				return
			}
			c.log.Error("error reading from WebSocket connection", slog.Any("error", err))
			go c.reconnect()
			return
		}
		typ, err := readHeader(payload)
		if err != nil {
			c.log.Error("error reading header", slog.Any("error", err))
			continue
		}
		if err := c.handleMessage(conn, typ, payload[1:]); err != nil {
			c.log.Error("error handling message", slog.Any("error", err))
			continue
		}
	}
}

// reconnect re-establishes the WebSocket connection. Only one reconnect may
// run at a time. Concurrent calls after the first are no-ops. If establishment fails,
// the Conn is closed with the error as the cause.
func (c *Conn) reconnect() {
	if c.ctx.Err() != nil {
		return
	}
	if !c.reconnecting.CompareAndSwap(false, true) {
		return
	}
	defer c.reconnecting.Store(false)

	c.log.Info("re-establishing WebSocket connection...")

	done := make(chan struct{})
	c.reconnectMu.Lock()
	c.reconnectDone = done
	c.reconnectMu.Unlock()
	defer func() {
		c.reconnectMu.Lock()
		c.reconnectDone = nil
		c.reconnectMu.Unlock()
		close(done)
	}()

	interruptedAttempts := 0
	for {
		conn, err := c.dialer.reconnect(c.ctx)
		if err != nil {
			c.log.Error("error re-establishing WebSocket connection", slog.Any("error", err))
			_ = c.close(fmt.Errorf("rta: reconnect: %w", err))
			return
		}
		c.connMu.Lock()
		c.conn = conn
		c.connMu.Unlock()
		go c.read(conn)

		c.subscriptionsMu.Lock()
		subscriptions := make([]*Subscription, 0, len(c.subscriptions))
		for _, subscription := range c.subscriptions {
			if subscription.shouldResubscribe() {
				subscriptions = append(subscriptions, subscription)
			}
		}
		clear(c.subscriptions)
		c.subscriptionsMu.Unlock()

		c.log.Info("resubscribing existing subscriptions...", slog.Int("count", len(subscriptions)))
		if c.resubscribe(subscriptions) {
			interruptedAttempts++
			if interruptedAttempts >= maxReconnectAttempts {
				err := fmt.Errorf("resubscribe interrupted after %d reconnect attempts", interruptedAttempts)
				c.log.Error("error re-establishing WebSocket connection", slog.Any("error", err))
				_ = c.close(fmt.Errorf("rta: reconnect: %w", err))
				return
			}
			_ = conn.Close(websocket.StatusGoingAway, "resubscribe interrupted")
			c.log.Info("resubscribe interrupted; reconnecting again")
			continue
		}
		return
	}
}

// resubscribe re-establishes all subscriptions inherited from the previous
// WebSocket connection. Each re-subscribe attempt has a timeout of 15 seconds.
// Terminal subscription failures are reported via [SubscriptionHandler.HandleError].
// It reports whether any subscription was interrupted by a lost connection and
// should be retried by another reconnect attempt.
func (c *Conn) resubscribe(subscriptions []*Subscription) (interrupted bool) {
	var successCount atomic.Int32
	var interruptedSubscription atomic.Bool
	var wg sync.WaitGroup
	for _, subscription := range subscriptions {
		wg.Go(func() {
			log := c.log.With(slog.Group("subscription",
				slog.Uint64("id", uint64(subscription.ID())),
				slog.String("resourceURI", subscription.ResourceURI()),
			))

			ctx, cancel := context.WithTimeout(c.ctx, time.Second*15)
			defer cancel()
			subscription.opMu.Lock()
			err := c.subscribe(ctx, subscription)
			subscription.opMu.Unlock()
			if err != nil {
				if err == errConnectionInterrupted {
					c.trackSubscription(subscription)
					interruptedSubscription.Store(true)
					log.Error("resubscribe interrupted", slog.Any("error", err))
					return
				}
				subscription.deactivate(fmt.Errorf("resubscribe: %w", err))
				log.Error("error resubscribing", slog.Any("error", err))
				return
			}

			c.trackSubscription(subscription)
			successCount.Add(1)

			c.log.Debug("resubscribed", slog.Group("subscription",
				slog.Uint64("id", uint64(subscription.ID())),
				slog.String("custom", string(subscription.Custom())),
				slog.String("resourceURI", subscription.ResourceURI()),
			))
		})
	}

	wg.Wait()
	c.log.Info("resubscribed existing subscriptions",
		slog.Int("success", int(successCount.Load())),
		slog.Int("total", len(subscriptions)),
	)
	return interruptedSubscription.Load()
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

		notifyErr := cause
		if !errors.Is(notifyErr, net.ErrClosed) {
			notifyErr = errors.Join(notifyErr, net.ErrClosed)
		}
		c.subscriptionsMu.RLock()
		for _, subscription := range c.subscriptions {
			subscription.deactivate(notifyErr)
		}
		c.subscriptionsMu.RUnlock()
	})
	return err
}

// handleMessage handles a message received in read with the type.
func (c *Conn) handleMessage(conn *websocket.Conn, typ uint32, payload []json.RawMessage) error {
	switch typ {
	case typeSubscribe, typeUnsubscribe: // Subscribe & Unsubscribe handshake response
		resp, err := readResponse(payload)
		if err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		op := typeToOperation(typ)
		c.expectedMu.Lock()
		expected, ok := c.expected[op][resp.sequence]
		if ok && expected.conn == conn {
			delete(c.expected[op], resp.sequence)
		}
		c.expectedMu.Unlock()
		if !ok {
			return fmt.Errorf("unexpected response for operation %d with sequence %d", op, resp.sequence)
		}
		if expected.conn != conn {
			return fmt.Errorf("unexpected response for operation %d with sequence %d on stale connection", op, resp.sequence)
		}
		select {
		case expected.ch <- resp:
			return nil
		case <-c.ctx.Done():
			return c.ctx.Err()
		default:
			return fmt.Errorf("channel buffer is full")
		}
	case typeEvent:
		if len(payload) < 2 {
			return errors.New("event message has no custom data")
		}
		var subscriptionID uint32
		if err := json.Unmarshal(payload[0], &subscriptionID); err != nil {
			return fmt.Errorf("decode subscription ID: %w", err)
		}
		c.subscriptionsMu.RLock()
		sub, ok := c.subscriptions[subscriptionID]
		c.subscriptionsMu.RUnlock()
		if ok && sub.Active() {
			go sub.handleEvent(payload[1])
		}
		c.log.Debug("received event", slog.Group("message", "type", typ, "custom", payload[0]))
		return nil
	case typeResync:
		c.log.Debug("received resync")
		c.subscriptionsMu.RLock()
		for _, subscription := range c.subscriptions {
			if subscription.Active() {
				go subscription.handleResync()
			}
		}
		c.subscriptionsMu.RUnlock()
		return nil
	default:
		return fmt.Errorf("unknown message type: %d", typ)
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

// readResponse decodes a response from the first 2 values from the payload.
// An [OutOfRangeError] if the payload length is not sufficient for reading.
func readResponse(payload []json.RawMessage) (*response, error) {
	if len(payload) < 2 {
		return nil, &OutOfRangeError{
			Payload: payload,
			Index:   2,
		}
	}
	resp := &response{}
	if err := json.Unmarshal(payload[0], &resp.sequence); err != nil {
		return nil, fmt.Errorf("decode sequence: %w", err)
	}
	if err := json.Unmarshal(payload[1], &resp.status); err != nil {
		return nil, fmt.Errorf("decode status code: %w", err)
	}
	resp.payload = payload[2:]
	return resp, nil
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
