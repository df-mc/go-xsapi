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

// Dial establishes a connection with real-time activity service.
//
// The [context.Context] is used to control the deadline of the establishment of the WebSocket connection.
// The [http.Client] is used to authenticate handshake HTTP requests and is typically retrieved from
// [github.com/df-mc/go-xsapi.Client.HTTPClient].
func Dial(ctx context.Context, client *http.Client, log *slog.Logger) (*Conn, error) {
	d := newDialer(client, log)
	c, err := d.dial(ctx)
	if err != nil {
		return nil, err
	}
	return newConn(c, d), nil
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

func newDialer(client *http.Client, log *slog.Logger) *dialer {
	if log == nil {
		log = slog.Default()
	}
	return &dialer{
		log: log,
		options: &websocket.DialOptions{
			Subprotocols: []string{subprotocol},
			HTTPClient:   client,
		},
	}
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

// dialWithBackoff attempts to establish a WebSocket connection with the RTA service.
// It retries up to maxDialAttempts times, waiting between each attempt with
// exponential backoff and jitter. If the context is canceled, it returns the
// context error immediately.
func (d *dialer) dialWithBackoff(ctx context.Context) (*websocket.Conn, error) {
	for attempt := range maxDialAttempts {
		c, err := d.dial(ctx)
		if err != nil {
			sleep := backoffDuration(attempt)
			d.log.Error("error re-establishing WebSocket connection",
				slog.Int("attempt", attempt), slog.Int("maxAttempts", maxDialAttempts),
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
	return nil, fmt.Errorf("max reconnect attempt (%d) reached", maxDialAttempts)
}

// backoffDuration returns the duration to wait before the next reconnect attempt.
// The base duration doubles with each attempt with up to 50% additional jitter.
func backoffDuration(attempt int) time.Duration {
	base := time.Second << attempt
	jitter := time.Duration(rand.Int63n(int64(base / 2)))
	return base + jitter
}

// maxDialAttempts is the maximum number of reconnect attempts before
// [dialer.dialWithBackoff] gives up and returns an error.
const maxDialAttempts = 4

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
