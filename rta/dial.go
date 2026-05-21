package rta

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/coder/websocket"
)

// Dialer represents the options for establishing a Conn with real-time activity services.
type Dialer struct {
	Options  *websocket.DialOptions
	ErrorLog *slog.Logger
}

// Dial calls DialContext with a 15 second timeout.
func (d Dialer) Dial(client *http.Client) (*Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return d.DialContext(ctx, client)
}

// DialContext establishes a connection with real-time activity service.
func (d Dialer) DialContext(ctx context.Context, client *http.Client) (*Conn, error) {
	log := d.ErrorLog
	if log == nil {
		log = slog.Default()
	}

	internalDialer := &dialer{
		client:  client,
		log:     log,
		options: d.Options,
	}
	c, err := internalDialer.dial(ctx)
	if err != nil {
		return nil, err
	}
	conn := &Conn{
		conn:          c,
		dialer:        internalDialer,
		log:           log,
		subscriptions: make(map[uint32]*Subscription),
		pending:       make(map[*Subscription]struct{}),
	}
	conn.ctx, conn.cancel = context.WithCancelCause(context.Background())
	for i := range cap(conn.expected) {
		conn.expected[i] = make(map[uint32]chan<- *handshake)
	}
	conn.startReader(c)
	return conn, nil
}

type dialer struct {
	client  *http.Client
	log     *slog.Logger
	options *websocket.DialOptions
}

// dial establishes a new WebSocket connection.
func (d *dialer) dial(ctx context.Context) (*websocket.Conn, error) {
	options := websocket.DialOptions{}
	if d.options != nil {
		options = *d.options
	}
	options.HTTPClient = d.client
	options.Subprotocols = slices.Clone(options.Subprotocols)
	if !slices.Contains(options.Subprotocols, subprotocol) {
		options.Subprotocols = append(options.Subprotocols, subprotocol)
	}

	c, _, err := websocket.Dial(ctx, connectURL.String(), &options)
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
