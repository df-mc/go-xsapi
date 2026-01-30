package rta

import (
	"context"
	"log/slog"
	"net/url"
	"slices"
	"time"

	"github.com/coder/websocket"
	"github.com/yomoggies/xsapi-go/internal"
)

// Dialer represents the options for establishing a Conn with real-time activity services with DialContext or Dial.
type Dialer struct {
	Options  *websocket.DialOptions
	ErrorLog *slog.Logger
}

type API interface {
	internal.HTTPClient
}

// Dial calls DialContext with a 15 seconds timeout.
func (d Dialer) Dial(api API) (*Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return d.DialContext(ctx, api)
}

// DialContext establishes a connection with real-time activity service. A context.Context is used to control the
// scene real-timely. An authorization token may be used for configuring an HTTP header to Options. An error may be
// returned during the dial of websocket connection.
func (d Dialer) DialContext(ctx context.Context, api API) (*Conn, error) {
	if d.ErrorLog == nil {
		d.ErrorLog = slog.Default()
	}
	if d.Options == nil {
		d.Options = &websocket.DialOptions{}
	}
	d.Options.HTTPClient = api.HTTPClient()
	if !slices.Contains(d.Options.Subprotocols, subprotocol) {
		d.Options.Subprotocols = append(d.Options.Subprotocols, subprotocol)
	}

	c, _, err := websocket.Dial(ctx, connectURL.String(), d.Options)
	if err != nil {
		return nil, err
	}
	conn := &Conn{
		conn:          c,
		log:           d.ErrorLog,
		subscriptions: make(map[uint32]*Subscription),
		closed:        make(chan struct{}),
	}
	for i := range cap(conn.expected) {
		conn.expected[i] = make(map[uint32]chan<- *handshake)
	}
	go conn.read()
	return conn, nil
}

const (
	// subprotocol is the subprotocol used with connectURL, to establish a websocket connection.
	subprotocol = "rta.xboxlive.com.V2"
)

// connectURL is the URL used to establish a websocket connection with real-time activity services. It is
// generally present at websocket.Dial with other websocket.DialOptions, specifically along with subprotocol.
var connectURL = &url.URL{
	Scheme: "wss",
	Host:   "rta.xboxlive.com",
	Path:   "connect",
}
