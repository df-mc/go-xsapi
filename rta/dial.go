package rta

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v7"
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
	// backoff creates a separate retry schedule for each reconnect loop.
	backoff func() backoff.BackOff
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
		backoff: reconnectBackoff,
	}
}

// reconnectBackoff is the backoff schedule new dialers use; tests shorten it.
var reconnectBackoff = newReconnectBackoff

// dial establishes a new WebSocket connection.
func (d *dialer) dial(ctx context.Context) (*websocket.Conn, error) {
	options := *d.options
	options.Subprotocols = slices.Clone(d.options.Subprotocols)
	c, _, err := websocket.Dial(ctx, connectURLString(), &options)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// reconnect re-establishes the WebSocket connection, retrying with capped
// exponential backoff until it succeeds or ctx is done. A service outage can
// outlast any fixed attempt budget, and a Conn that gave up would strand every
// subscription until the caller noticed, so only ctx ends the retries.
func (d *dialer) reconnect(ctx context.Context) (*websocket.Conn, error) {
	attempt := 0
	return backoff.Retry(ctx, func() (*websocket.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, backoff.Permanent(err)
		}
		c, err := d.dial(ctx)
		if err == nil {
			d.log.Debug("reconnected to RTA service", slog.Int("attempt", attempt))
		}
		return c, err
	}, backoff.WithBackOff(d.backoff()), backoff.WithMaxElapsedTime(0), backoff.WithNotify(func(err error, sleep time.Duration) {
		// The first failure is news; a long outage should not be an Error stream.
		level := slog.LevelWarn
		if attempt == 0 {
			level = slog.LevelError
		}
		d.log.Log(ctx, level, "error re-establishing WebSocket connection",
			slog.Any("error", err), slog.Int("attempt", attempt), slog.Duration("sleep", sleep),
		)
		attempt++
	}))
}

// newReconnectBackoff preserves waits of 1, 2, 4, 8, 16, 32, then 60 seconds,
// plus up to 50% jitter. A 20% spread around 1.25 times each base interval
// produces the same range. Each retry loop must own its mutable schedule.
func newReconnectBackoff() backoff.BackOff {
	return &backoff.ExponentialBackOff{
		InitialInterval:     1250 * time.Millisecond,
		RandomizationFactor: 0.2,
		Multiplier:          2,
		MaxInterval:         75 * time.Second,
	}
}

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
