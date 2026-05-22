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
	reconnectBackoffMax           = 60 * time.Second
	reconnectBackoffJitterMax     = 5 * time.Second
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

	subscriptions subscriptionRegistry

	log *slog.Logger

	// reconnect tracks the active reconnect/resubscribe wave.
	reconnectState reconnectState

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
		if !c.reconnectWaveStable(readerDone) {
			c.subscriptions.remove(sub)
			continue
		}
		// The subscribe ACK hook already registers the subscription on the normal
		// path before events can arrive. Keep this update for test hooks and as a
		// harmless final consistency write after the reconnect-stability gate.
		c.subscriptions.update(sub, sub.id())
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

// resubscribeDuringReconnect re-establishes an existing subscription during a
// reconnect wave. The successful ACK is applied and routed before it is
// delivered to this caller so events sent immediately after the ACK use the new
// service-side subscription ID.
func (c *Conn) resubscribeDuringReconnect(ctx context.Context, subscription *Subscription) error {
	h, _, err := c.callWithHook(ctx, operationSubscribe, func() []any {
		return []any{subscription.resourceURI}
	}, false, func(h *handshake) {
		if err := c.applySubscribeHandshake(subscription, h); err == nil {
			c.updateSubscriptionID(subscription, subscription.id())
		}
	})
	if errors.Is(err, errReconnectInterrupted) {
		c.markReconnectNext()
	}
	if err != nil {
		return err
	}
	if err := c.applySubscribeHandshake(subscription, h); err != nil {
		return err
	}
	c.updateSubscriptionID(subscription, subscription.id())
	return nil
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
		sub.activate(id, h.payload[1])
		return nil
	default:
		return unexpectedStatusCode(h.status, h.payload)
	}
}

// Unsubscribe attempts to unsubscribe with a Subscription associated with an ID, with
// the [context.Context] to be used during the handshake. An error may be returned.
func (c *Conn) Unsubscribe(ctx context.Context, sub *Subscription) error {
	var id uint32
	h, _, err := c.callWithHook(ctx, operationUnsubscribe, func() []any {
		id = sub.id()
		return []any{id}
	}, true, nil)
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
		c.reconnectState.mu.RLock()
		done := c.reconnectState.done
		c.reconnectState.mu.RUnlock()

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
		sub, ok := c.subscriptions.get(subscriptionID)
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
