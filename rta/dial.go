package rta

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/coder/websocket"
)

// Dial establishes a connection with real-time activity service.
//
// The [context.Context] is used to control the deadline of the establishment of the WebSocket connection.
// The [http.Client] is used to authenticate handshake HTTP requests and is typically retrieved from
// [github.com/df-mc/go-xspai.Client.HTTPClient].
func Dial(ctx context.Context, client *http.Client, log *slog.Logger) (*Conn, error) {
	if log == nil {
		log = slog.Default()
	}

	d := &dialer{
		client: client,
		log:    log,
	}
	c, err := d.dial(ctx)
	if err != nil {
		return nil, err
	}
	conn := &Conn{
		conn:          c,
		dialer:        d,
		log:           log,
		subscriptions: make(map[uint32]*Subscription),
	}
	conn.ctx, conn.cancel = context.WithCancelCause(context.Background())
	for i := range cap(conn.expected) {
		conn.expected[i] = make(map[uint32]chan<- *handshake)
	}
	go conn.read()
	return conn, nil
}

type dialer struct {
	client *http.Client
	log    *slog.Logger
}

// dial establishes a new WebSocket connection.
func (d *dialer) dial(ctx context.Context) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, connectURL.String(), &websocket.DialOptions{
		Subprotocols: []string{subprotocol},
		HTTPClient:   d.client,
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// subprotocol is the subprotocol used with connectURL, to establish a websocket connection.
const subprotocol = "rta.xboxlive.com.V2"

// connectURL is the URL used to establish a websocket connection with real-time activity services. It is
// generally present at websocket.Dial with other websocket.DialOptions, specifically along with subprotocol.
var connectURL = &url.URL{
	Scheme: "wss",
	Host:   "rta.xboxlive.com",
	Path:   "connect",
}
