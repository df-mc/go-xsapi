package rta

import (
	"context"
	"encoding/json"
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
	connMu sync.Mutex

	dialer *dialer

	sequences  [operationCapacity]atomic.Uint32
	expected   [operationCapacity]map[uint32]chan<- *handshake
	expectedMu sync.RWMutex

	subscriptions   map[uint32]*Subscription
	subscriptionsMu sync.RWMutex

	log *slog.Logger

	reconnecting  atomic.Bool
	reconnectDone chan struct{}
	reconnectMu   sync.RWMutex

	once   sync.Once
	ctx    context.Context
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

func (c *Conn) subscribe(ctx context.Context, resourceURI string) (*Subscription, error) {
	for {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}

		sequence := c.sequences[operationSubscribe].Add(1)
		hand, err := c.expect(operationSubscribe, sequence, []any{resourceURI})
		if err != nil {
			continue
		}

		select {
		case h, ok := <-hand:
			if !ok {
				// drainExpected cleared the expected map and closed the channel
				// because the connection was lost during the handshake.
				// We can retry on the new connection.
				continue
			}

			c.release(operationSubscribe, sequence)
			switch h.status {
			case StatusOK:
				if len(h.payload) < 2 {
					return nil, &OutOfRangeError{
						Payload: h.payload,
						Index:   1,
					}
				}
				sub := &Subscription{resourceURI: resourceURI}
				if err := json.Unmarshal(h.payload[0], &sub.id); err != nil {
					return nil, fmt.Errorf("decode subscription ID: %w", err)
				}
				sub.custom = h.payload[1]
				return sub, nil
			default:
				return nil, unexpectedStatusCode(h.status, h.payload)
			}
		case <-ctx.Done():
			c.release(operationSubscribe, sequence)
			return nil, ctx.Err()
		case <-c.ctx.Done():
			c.release(operationSubscribe, sequence)
			return nil, context.Cause(c.ctx)
		}
	}
}

// Unsubscribe attempts to unsubscribe with a Subscription associated with an ID, with
// the [context.Context] to be used during the handshake. An error may be returned.
func (c *Conn) Unsubscribe(ctx context.Context, sub *Subscription) error {
	for {
		if err := c.wait(ctx); err != nil {
			return err
		}

		sequence := c.sequences[operationUnsubscribe].Add(1)
		hand, err := c.expect(operationUnsubscribe, sequence, []any{sub.ID()})
		if err != nil {
			continue
		}

		select {
		case h, ok := <-hand:
			if !ok {
				continue
			}
			c.release(operationUnsubscribe, sequence)
			if h.status != StatusOK {
				return unexpectedStatusCode(h.status, h.payload)
			}
			c.subscriptionsMu.Lock()
			delete(c.subscriptions, sub.ID())
			c.subscriptionsMu.Unlock()
			return nil
		case <-ctx.Done():
			c.release(operationUnsubscribe, sequence)
			return ctx.Err()
		case <-c.ctx.Done():
			c.release(operationUnsubscribe, sequence)
			return context.Cause(c.ctx)
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

func (s *Subscription) ID() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

func (s *Subscription) Custom() json.RawMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.custom
}

func (s *Subscription) ResourceURI() string {
	return s.resourceURI
}

func (s *Subscription) Handle(h SubscriptionHandler) {
	s.mu.Lock()
	s.h = h
	s.mu.Unlock()
}

func (s *Subscription) handler() SubscriptionHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.h == nil {
		return NopSubscriptionHandler{}
	}
	return s.h
}

type SubscriptionHandler interface {
	HandleEvent(custom json.RawMessage)
	HandleReconnect()
}

type NopSubscriptionHandler struct{}

func (NopSubscriptionHandler) HandleEvent(json.RawMessage) {}
func (NopSubscriptionHandler) HandleReconnect()            {}

// write attempts to write a JSON array with header and the body. A background context is
// used as no context perceived by the parent goroutine should be used to a websocket method
// to avoid closing the connection if it has cancelled or exceeded a deadline.
func (c *Conn) write(typ uint32, payload []any) error {
	return wsjson.Write(context.Background(), c.conn, append([]any{typ}, payload...))
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

// drainExpected closes all channels currently registered in c.expected
// and clears the map.
func (c *Conn) drainExpected() {
	c.expectedMu.Lock()
	for op := range operationCapacity {
		for seq, ch := range c.expected[op] {
			close(ch)
			delete(c.expected[op], seq)
		}
	}
	c.expectedMu.Unlock()
}

// read goes as a background goroutine of Conn, reading a JSON array from the websocket
// connection and decoding a header needed to indicate which message should be handled.
func (c *Conn) read() {
	defer c.drainExpected()

	for {
		var payload []json.RawMessage
		if err := wsjson.Read(context.Background(), c.conn, &payload); err != nil {
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
		go c.handleMessage(typ, payload[1:])
	}
}

func (c *Conn) reconnect() {
	if c.ctx.Err() != nil {
		return
	}
	if !c.reconnecting.CompareAndSwap(false, true) {
		return
	}
	defer c.reconnecting.Store(false)

	c.log.Info("reconnecting WebSocket connection...")

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

	conn, err := c.dialer.dial(c.ctx)
	if err != nil {
		c.log.Error("error re-establishing WebSocket connection", slog.Any("error", err))
		_ = c.close(fmt.Errorf("rta: reconnect: %w", err))
		return
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
	go c.read()

	c.log.Info("reconnected, resubscribing any existing subscriptions")
	go c.resubscribe()
}

func (c *Conn) resubscribe() {
	c.subscriptionsMu.Lock()
	subscriptions := make([]*Subscription, 0, len(c.subscriptions))
	for _, s := range c.subscriptions {
		subscriptions = append(subscriptions, s)
	}
	clear(c.subscriptions)
	c.subscriptionsMu.Unlock()

	wg := new(sync.WaitGroup)
	wg.Add(len(subscriptions))
	for _, sub := range subscriptions {
		go func(sub *Subscription) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(c.ctx, time.Second*15)
			defer cancel()

			newSub, err := c.subscribe(ctx, sub.ResourceURI())
			if err != nil {
				c.log.Error("error resubscribing",
					slog.Group("subscription",
						slog.Uint64("id", uint64(sub.ID())),
						slog.String("resourceURI", sub.ResourceURI()),
					),
					slog.Any("error", err),
				)
				return
			}

			sub.mu.Lock()
			sub.id = newSub.id
			sub.custom = newSub.custom
			sub.mu.Unlock()

			c.subscriptionsMu.Lock()
			c.subscriptions[sub.id] = sub
			c.subscriptionsMu.Unlock()

			// Call HandleReconnect() to notice that the subscription has been refreshed in the new connection.
			// This is because the subscription's custom data may differ from the previous one.
			go sub.handler().HandleReconnect()
		}(sub)
	}

	wg.Wait()
	c.log.Info("resubscribed any existing subscriptions")
}

// Close closes the websocket connection with websocket.StatusNormalClosure.
func (c *Conn) Close() (err error) {
	return c.close(net.ErrClosed)
}

// close cancels the background context of the Conn.
func (c *Conn) close(cause error) (err error) {
	c.once.Do(func() {
		c.cancel(cause)
		err = c.conn.Close(websocket.StatusNormalClosure, "")
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
		c.expectedMu.RLock()
		hand, ok := c.expected[op][h.sequence]
		c.expectedMu.RUnlock()
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
