package rta

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Dialer represents the options for establishing a Conn with real-time
// activity services with DialContext or Dial.
type Dialer struct {
	Options  *websocket.DialOptions
	ErrorLog *slog.Logger
}

// Dial calls DialContext with a 15 seconds timeout.
func (d Dialer) Dial(client *http.Client) (*Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return d.DialContext(ctx, client)
}

// DialContext establishes a connection with real-time activity service.
func (d Dialer) DialContext(ctx context.Context, client *http.Client) (*Conn, error) {
	dialer := d.dialer(client)
	c, err := dialer.dial(ctx)
	if err != nil {
		return nil, err
	}
	return newConn(c, dialer), nil
}

func (d Dialer) dialer(client *http.Client) *dialer {
	log := d.ErrorLog
	if log == nil {
		log = slog.Default()
	}
	options := &websocket.DialOptions{}
	if d.Options != nil {
		*options = *d.Options
		options.Subprotocols = slices.Clone(d.Options.Subprotocols)
	}
	options.HTTPClient = client
	if !slices.Contains(options.Subprotocols, subprotocol) {
		options.Subprotocols = append(options.Subprotocols, subprotocol)
	}
	return &dialer{
		log:     log,
		options: options,
	}
}

// Dial establishes a connection with real-time activity service.
//
// The [context.Context] is used to control the deadline of the establishment of the WebSocket connection.
// The [http.Client] is used to authenticate handshake HTTP requests and is typically retrieved from
// [github.com/df-mc/go-xsapi.Client.HTTPClient].
func Dial(ctx context.Context, client *http.Client, log *slog.Logger) (*Conn, error) {
	return Dialer{ErrorLog: log}.DialContext(ctx, client)
}

func newConn(c *websocket.Conn, d *dialer) *Conn {
	conn := &Conn{
		conn:          c,
		dialer:        d,
		log:           d.log,
		subscriptions: make(map[uint32]*Subscription),
	}
	conn.ctx, conn.cancel = context.WithCancelCause(context.Background())
	for i := range cap(conn.expected) {
		conn.expected[i] = make(map[uint32]expectedCall)
	}
	go conn.read(c)
	return conn
}

type dialer struct {
	log     *slog.Logger
	options *websocket.DialOptions
}

// dial establishes a new WebSocket connection.
func (d *dialer) dial(ctx context.Context) (*websocket.Conn, error) {
	options := *d.options
	options.Subprotocols = slices.Clone(d.options.Subprotocols)
	c, resp, err := websocket.Dial(ctx, connectURLString(), &options)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return nil, err
	}
	return c, nil
}

// reconnect attempts to re-establish a WebSocket connection with the RTA service.
// It retries up to maxReconnectAttempts times, waiting between each attempt with
// exponential backoff and jitter. If the context is canceled, it returns the
// context error immediately.
func (d *dialer) reconnect(ctx context.Context) (*websocket.Conn, error) {
	for attempt := 0; attempt < maxReconnectAttempts; attempt++ {
		c, err := d.dial(ctx)
		if err != nil {
			sleep := backoffDuration(attempt)
			d.log.Error("error re-establishing WebSocket connection",
				slog.Int("attempt", attempt), slog.Int("maxAttempts", maxReconnectAttempts),
				slog.Duration("sleep", sleep),
			)
			select {
			case <-time.After(sleep):
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		d.log.Debug("reconnected to RTA service", slog.Int("attempt", attempt))
		return c, nil
	}
	return nil, fmt.Errorf("max reconnect attempt (%d) reached", maxReconnectAttempts)
}

// backoffDuration returns the duration to wait before the next reconnect attempt.
// The base duration doubles with each attempt with up to 50% additional jitter.
func backoffDuration(attempt int) time.Duration {
	base := time.Second << attempt
	jitter := time.Duration(rand.Int63n(int64(base / 2)))
	return base + jitter
}

// maxReconnectAttempts is the maximum number of reconnect attempts before
// [dialer.reconnect] gives up and returns an error.
const maxReconnectAttempts = 4

// subprotocol is the subprotocol used with connectURL, to establish a websocket connection.
const subprotocol = "rta.xboxlive.com.V2"

var connectURLMu sync.RWMutex

// connectURL is the URL used to establish a websocket connection with real-time activity services. It is
// generally present at websocket.Dial with other websocket.DialOptions, specifically along with subprotocol.
var connectURL = &url.URL{
	Scheme: "wss",
	Host:   "rta.xboxlive.com",
	Path:   "connect",
}

func connectURLString() string {
	connectURLMu.RLock()
	defer connectURLMu.RUnlock()
	return connectURL.String()
}
