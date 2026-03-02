package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

type Conn struct {
	conn   *websocket.Conn
	client *http.Client
	log    *slog.Logger

	seq     atomic.Uint32
	calls   map[uint32]chan<- *ServerEnvelope
	callsMu sync.Mutex

	// id is the unique identifier assigned for the Conn,
	// retrieved via a WhoAmI call during Dial.
	id uuid.UUID
	// nonce is the nonce used to authenticate the Conn
	// on external services.
	nonce string

	handlers   []Handler
	handlersMu sync.RWMutex

	ctx    context.Context
	cancel context.CancelCauseFunc
	once   sync.Once
}

func (c *Conn) ConnectionID() uuid.UUID {
	return c.id
}

func (c *Conn) Nonce() string {
	return c.nonce
}

func (c *Conn) Call(ctx context.Context, message clientRequest) (*ServerEnvelope, error) {
	seq, ch := c.seq.Add(1), make(chan *ServerEnvelope)
	message.prepareEnvelope(message.MessageType(), seq)

	if err := wsjson.Write(ctx, c.conn, message); err != nil {
		return nil, err
	}

	c.callsMu.Lock()
	c.calls[seq] = ch
	c.callsMu.Unlock()

	defer func() {
		c.callsMu.Lock()
		delete(c.calls, seq)
		c.callsMu.Unlock()
	}()

	select {
	case <-c.ctx.Done():
		return nil, context.Cause(c.ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	case envelope := <-ch:
		return envelope, nil
	}
}

func (c *Conn) AddHandler(h Handler) {
	c.handlersMu.Lock()
	c.handlers = append(c.handlers, h)
	c.handlersMu.Unlock()
}

type Handler interface {
	HandleServerChatMessage(envelope *ServerEnvelope)
}

func (c *Conn) whoAmI(ctx context.Context) (*ServerEnvelope, *whoAmIResult, error) {
	envelope, err := c.Call(ctx, whoAmIRequest{
		ClientEnvelope: &ClientEnvelope{
			Channel: &Channel{
				Type: "System",
			},
		},
	})
	if err != nil {
		return nil, nil, err
	}
	if envelope.Type != MessageTypeWhoAmI {
		return nil, nil, fmt.Errorf("xsapi/chat: unexpected response type: %q, expected %q", envelope.Type, MessageTypeWhoAmI)
	}
	var result *whoAmIResult
	if err := json.Unmarshal(envelope.Raw, &result); err != nil {
		return nil, nil, fmt.Errorf("xsapi/chat: decode whoAmIResult: %w", err)
	}
	return envelope, result, nil
}

func (c *Conn) background() {
	for {
		_, reader, err := c.conn.Reader(c.ctx)
		if err != nil {
			_ = c.close(err)
			return
		}

		var envelope *ServerEnvelope
		if err := json.NewDecoder(reader).Decode(&envelope); err != nil {
			c.log.Error("error decoding server envelope", slog.Any("error", err))
			continue
		}
		if envelope.Type == MessageTypeNoOp {
			continue
		}
		if envelope.ServerOriginated {
			c.handlersMu.RLock()
			for _, h := range c.handlers {
				go h.HandleServerChatMessage(envelope)
			}
			c.handlersMu.RUnlock()
			continue
		}

		c.callsMu.Lock()
		ch, ok := c.calls[envelope.Sequence]
		if ok {
			select {
			case ch <- envelope:
			default:
			}
		}
		c.callsMu.Unlock()

		if !ok {
			c.log.Error("received unexpected response", slog.Uint64("sequence", uint64(envelope.Sequence)))
			continue
		}
	}
}

func (c *Conn) Close() error {
	return c.close(net.ErrClosed)
}

func (c *Conn) close(cause error) (err error) {
	c.once.Do(func() {
		c.cancel(cause)
		err = c.conn.Close(websocket.StatusNormalClosure, "")
	})
	return err
}
