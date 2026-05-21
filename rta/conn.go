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
	expectHook func(op uint8, sequence uint32, payload []any) (<-chan *handshake, chan struct{}, error)

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
	// reconnectDone is a channel that is closed when the reconnect dial/
	// resubscribe wave is complete. It is nil when no reconnect is in progress.
	reconnectDone chan struct{}
	// reconnectHandlersDone is closed when all asynchronous reconnect failure
	// handlers have finished. It is nil when no such handlers are running.
	reconnectHandlersDone chan struct{}
	reconnectHandlers     int
	// reconnectMu guards reconnectDone and reconnect failure-handler tracking.
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
	for {
		sub, readerDone, err := c.subscribe(ctx, resourceURI)
		if err != nil {
			return nil, err
		}
		c.subscriptionsMu.Lock()
		if !c.reconnectWaveStable(readerDone) {
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
	h, readerDone, err := c.callWithPayload(ctx, operationSubscribe, func() []any {
		return []any{resourceURI}
	})
	if err != nil {
		return nil, nil, err
	}
	sub, err := c.readSubscribeHandshake(resourceURI, h)
	return sub, readerDone, err
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
		if err := json.Unmarshal(h.payload[0], &sub.ID); err != nil {
			return nil, fmt.Errorf("decode subscription ID: %w", err)
		}
		sub.Custom = h.payload[1]
		sub.setCurrent(sub.ID, sub.Custom)
		return sub, nil
	default:
		return nil, unexpectedStatusCode(h.status, h.payload)
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
	c.subscriptionsMu.Lock()
	delete(c.subscriptions, id)
	c.subscriptionsMu.Unlock()
	return nil
}

var errReconnectInterrupted = errors.New("rta: reconnect interrupted")

// callWithPayload sends a sequenced message to the server and blocks using the
// given [context.Context] until the server responds with a matching sequence
// number. If the Conn is reconnecting, callWithPayload blocks until reconnect
// completes before sending.
func (c *Conn) callWithPayload(ctx context.Context, op uint8, payload func() []any) (*handshake, chan struct{}, error) {
	for {
		if err := c.wait(ctx); err != nil {
			return nil, nil, err
		}

		seq := c.sequences[op].Add(1)
		ch, readerDone, err := c.expect(op, seq, payload())
		if err != nil {
			continue
		}
		select {
		case result, ok := <-ch:
			if !ok {
				continue
			}
			c.release(op, seq)
			return result, readerDone, nil
		case <-ctx.Done():
			c.release(op, seq)
			return nil, nil, ctx.Err()
		case <-c.ctx.Done():
			c.release(op, seq)
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
	seq := c.sequences[op].Add(1)
	ch, _, err := c.expect(op, seq, payload)
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

// Subscription represents a subscription contracted with the resource URI available through
// the real-time activity service. A Subscription may be contracted via Conn.Subscribe.
type Subscription struct {
	// ID is the ID assigned to the Subscription within a single RTA connection.
	ID uint32
	// Custom is the custom data associated with the Subscription.
	//
	// The format and semantics of this data depend on the resource the
	// subscription is targeting. It is received alongside the successful
	// subscription response when the Subscription was established. The custom
	// data may change if the Conn has reconnected to RTA service.
	Custom json.RawMessage

	currentID     uint32
	currentCustom json.RawMessage
	currentSet    bool
	resourceURI   string

	h  SubscriptionHandler
	mu sync.RWMutex
}

func (s *Subscription) id() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.currentSet {
		return s.currentID
	}
	return s.ID
}

func (s *Subscription) setCurrent(id uint32, custom json.RawMessage) {
	s.mu.Lock()
	s.currentID = id
	s.currentCustom = custom
	s.currentSet = true
	s.mu.Unlock()
}

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

// wait blocks until any in-progress reconnect attempt and its tracked failure
// handlers have finished.
func (c *Conn) wait(ctx context.Context) error {
	for {
		c.reconnectMu.RLock()
		done := c.reconnectDone
		handlersDone := c.reconnectHandlersDone
		c.reconnectMu.RUnlock()

		if done == nil && handlersDone == nil {
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
		case <-handlersDone:
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
			close(ch)
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

		dialCtx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
		conn, err := c.dialer.dial(dialCtx)
		cancel()
		if err != nil {
			cause := fmt.Errorf("rta: reconnect: %w", err)
			c.log.Error("error re-establishing WebSocket connection", slog.Any("error", err))
			c.notifyReconnectFailure(cause)
			_ = c.close(cause)
			return
		}
		c.startReader(conn)

		successes := c.resubscribe()
		readerDone := c.currentReaderDone()
		for _, subscription := range successes {
			c.startReconnectSuccess(subscription)
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

func (c *Conn) notifyReconnectFailure(err error) {
	for _, subscription := range c.takeSubscriptionsForReconnect() {
		c.startReconnectFailureHandler()
		go func(subscription *Subscription) {
			defer c.finishReconnectFailureHandler()
			c.notifyReconnect(subscription, err)
		}(subscription)
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

			sub, err := c.subscribeDuringReconnect(ctx, subscription.resourceURI)
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
				c.startReconnectFailureHandler()
				go func(subscription *Subscription, err error) {
					defer c.finishReconnectFailureHandler()
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

			subscriptionID := sub.id()
			subscription.setCurrent(subscriptionID, sub.custom())

			c.subscriptionsMu.Lock()
			c.subscriptions[subscriptionID] = subscription
			c.subscriptionsMu.Unlock()

			successesMu.Lock()
			successes = append(successes, subscription)
			successesMu.Unlock()
		}(s)
	}

	wg.Wait()
	c.log.Info("resubscribed existing subscriptions",
		slog.Int("attempted", len(subscriptions)),
		slog.Int("successful", len(successes)),
	)
	return successes
}

// reconnectWaveStable reports whether the reconnect wave still points at the
// same live reader channel and has not already been marked for another retry.
func (c *Conn) reconnectWaveStable(readerDone chan struct{}) bool {
	if c.reconnectNext.Load() {
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

func (c *Conn) startReconnectFailureHandler() {
	c.reconnectMu.Lock()
	if c.reconnectHandlers == 0 {
		c.reconnectHandlersDone = make(chan struct{})
	}
	c.reconnectHandlers++
	c.reconnectMu.Unlock()
}

func (c *Conn) finishReconnectFailureHandler() {
	c.reconnectMu.Lock()
	defer c.reconnectMu.Unlock()
	if c.reconnectHandlers == 0 {
		return
	}
	c.reconnectHandlers--
	if c.reconnectHandlers == 0 {
		close(c.reconnectHandlersDone)
		c.reconnectHandlersDone = nil
	}
}

// notifyReconnectSuccess delivers reconnect success as soon as the
// subscription handshake on the replacement connection has succeeded.
func (c *Conn) notifyReconnectSuccess(subscription *Subscription) {
	c.notifyReconnect(subscription, nil)
}

func (c *Conn) notifyReconnect(subscription *Subscription, err error) {
	handler, ok := subscription.handler().(ReconnectHandler)
	if !ok {
		return
	}
	handler.HandleReconnect(err)
}

// startReconnectSuccess launches notifyReconnectSuccess asynchronously.
func (c *Conn) startReconnectSuccess(subscription *Subscription) {
	go func() {
		c.notifyReconnectSuccess(subscription)
	}()
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
