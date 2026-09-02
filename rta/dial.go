package rta

import (
	"context"
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
	// backoff returns the wait before reconnect attempt n. Tests shorten it.
	backoff func(attempt int) time.Duration
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
		backoff: backoffDuration,
	}
}

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
	for attempt := 0; ; attempt++ {
		c, err := d.dial(ctx)
		if err == nil {
			d.log.Debug("reconnected to RTA service", slog.Int("attempt", attempt))
			return c, nil
		}
		sleep := d.backoff(attempt)
		d.log.Error("error re-establishing WebSocket connection",
			slog.Any("error", err), slog.Int("attempt", attempt), slog.Duration("sleep", sleep),
		)
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// backoffDuration returns the wait before reconnect attempt n: one second
// doubling per attempt up to maxReconnectBackoff, plus up to 50% jitter.
func backoffDuration(attempt int) time.Duration {
	base := min(time.Second<<min(attempt, maxBackoffShift), maxReconnectBackoff)
	jitter := time.Duration(rand.Int63n(int64(base / 2)))
	return base + jitter
}

const (
	maxReconnectBackoff = time.Minute
	// maxBackoffShift bounds the doubling before the cap so the shift never overflows.
	maxBackoffShift = 6
)

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
