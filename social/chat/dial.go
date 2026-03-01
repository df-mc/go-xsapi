package chat

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"
)

func Dial(ctx context.Context, client *http.Client, log *slog.Logger) (*Conn, error) {
	options := &websocket.DialOptions{
		HTTPClient:   client,
		Subprotocols: []string{subprotocol},
	}
	conn, _, err := websocket.Dial(ctx, connectURL, options)
	if err != nil {
		return nil, err
	}

	c := &Conn{
		conn:   conn,
		client: client,
		log:    log,

		calls: make(map[uint32]chan<- *ServerEnvelope),
	}
	c.ctx, c.cancel = context.WithCancelCause(context.Background())
	go c.background()

	_, payload, err := c.whoAmI(ctx)
	if err != nil {
		return nil, fmt.Errorf("xsapi/chat: call WhoAmI: %w", err)
	}
	c.id, c.nonce = payload.ConnectionID, payload.ServerNonce

	return c, nil
}

const (
	connectURL = "wss://chat.xboxlive.com/chat/connect"
	// /chat/auth
	// /users/xuid(xuid)/chat/connect?AuthKey=

	subprotocol = "chat"
)
