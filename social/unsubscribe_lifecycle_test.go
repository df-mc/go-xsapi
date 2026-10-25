package social

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

func TestCleanupRetriesFailedTeardownAndAllowsReuse(t *testing.T) {
	c, srv := newSocialRTATestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	keptEvents := make(chan string, 1)
	removedEvents := make(chan string, 1)
	keep := nonComparableSocialHandler{calls: keptEvents, data: []string{"keep"}}
	cleanupKeep, err := c.Subscribe(ctx, keep)
	if err != nil {
		t.Fatal(err)
	}
	cleanupRemove, err := c.Subscribe(ctx, nonComparableSocialHandler{calls: removedEvents, data: []string{"remove"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupRemove(ctx); err != nil {
		t.Fatal(err)
	}
	if srv.subscribeCount.Load() != 1 || srv.unsubscribeCount.Load() != 0 {
		t.Fatal("removing one handler changed the shared RTA subscription")
	}
	dispatch := &subscriptionHandler{Client: c, log: c.log}
	dispatch.HandleEvent(json.RawMessage(`{"NotificationType":"Added","Xuids":["1"]}`))
	select {
	case <-keptEvents:
	case <-ctx.Done():
		t.Fatal("remaining handler did not receive the event")
	}
	select {
	case <-removedEvents:
		t.Fatal("removed handler still received an event")
	case <-time.After(50 * time.Millisecond):
	}

	srv.unsubscribeStatus.Store(rta.StatusServiceUnavailable)
	if err := cleanupKeep(ctx); err == nil {
		t.Fatal("expected the RTA unsubscribe failure")
	}
	if len(c.subscriptionHandlers) != 0 || !c.subscription.Active() {
		t.Fatal("failed teardown must remove the handler but preserve the active subscription")
	}
	srv.unsubscribeStatus.Store(rta.StatusOK)
	if err := cleanupKeep(ctx); err != nil {
		t.Fatalf("retry teardown: %v", err)
	}
	if c.subscription.Active() || srv.unsubscribeCount.Load() != 2 {
		t.Fatal("retry did not release the orphaned RTA subscription")
	}
	if err := cleanupKeep(ctx); err != nil {
		t.Fatal(err)
	}
	if srv.unsubscribeCount.Load() != 2 {
		t.Fatal("repeated cleanup sent another RTA request")
	}

	cleanupNew, err := c.Subscribe(ctx, keep)
	if err != nil {
		t.Fatalf("reuse client: %v", err)
	}
	if err := cleanupKeep(ctx); err != nil {
		t.Fatal(err)
	}
	if !c.subscription.Active() || len(c.subscriptionHandlers) != 1 || srv.subscribeCount.Load() != 2 {
		t.Fatal("old cleanup removed the new registration")
	}
	if err := c.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cleanupNew(ctx); err != nil {
		t.Fatal(err)
	}
	if c.subscription.Active() || len(c.subscriptionHandlers) != 0 || srv.unsubscribeCount.Load() != 3 {
		t.Fatal("cleanup after CloseContext sent another RTA request")
	}
	cleanupAfterClose, err := c.Subscribe(ctx, keep)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupNew(ctx); err != nil {
		t.Fatal(err)
	}
	if len(c.subscriptionHandlers) != 1 || !c.subscription.Active() || srv.unsubscribeCount.Load() != 3 {
		t.Fatal("cleanup from before CloseContext removed a new registration")
	}
	if err := cleanupAfterClose(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupSeparatesDuplicateAndNonComparableHandlers(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    SubscriptionHandler
	}{
		{"same value", NopSubscriptionHandler{}},
		{"same pointer", &NopSubscriptionHandler{}},
		{"slice field", nonComparableSocialHandler{data: []string{"x"}}},
		{"slice in interface", interfaceSocialHandler{data: []string{"x"}}},
		{"map in interface", interfaceSocialHandler{data: map[string]int{"x": 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, srv := newSocialRTATestClient(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			first, err := c.Subscribe(ctx, tc.h)
			if err != nil {
				t.Fatal(err)
			}
			second, err := c.Subscribe(ctx, tc.h)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := first(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if len(c.subscriptionHandlers) != 1 || srv.unsubscribeCount.Load() != 0 {
				t.Fatal("repeated cleanup removed a separate registration")
			}
			if err := second(ctx); err != nil {
				t.Fatal(err)
			}
			if len(c.subscriptionHandlers) != 0 || srv.unsubscribeCount.Load() != 1 {
				t.Fatal("final cleanup did not release the subscription")
			}
		})
	}
}

func TestConcurrentCleanupPreservesSharedSubscription(t *testing.T) {
	c, srv := newSocialRTATestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	keep := &interfaceSocialHandler{data: "keep"}
	cleanupKeep, err := c.Subscribe(ctx, keep)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := &subscriptionHandler{Client: c, log: c.log}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for id := range 8 {
		wg.Go(func() {
			h := &interfaceSocialHandler{data: id}
			for range 16 {
				cleanup, err := c.Subscribe(ctx, h)
				if err != nil {
					errs <- err
					return
				}
				dispatch.HandleEvent(json.RawMessage(`{"NotificationType":"Added","Xuids":["1"]}`))
				if err := cleanup(ctx); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(c.subscriptionHandlers) != 1 || c.subscriptionHandlers[0].SubscriptionHandler != keep {
		t.Fatal("concurrent cleanup changed the persistent handler")
	}
	if srv.subscribeCount.Load() != 1 || srv.unsubscribeCount.Load() != 0 {
		t.Fatal("concurrent handlers replaced the shared RTA subscription")
	}
	var failures atomic.Int32
	for range 8 {
		wg.Go(func() {
			if err := cleanupKeep(ctx); err != nil {
				failures.Add(1)
			}
		})
	}
	wg.Wait()
	if failures.Load() != 0 || srv.unsubscribeCount.Load() != 1 || c.subscription.Active() {
		t.Fatal("concurrent final cleanup did not release exactly one shared subscription")
	}
}

// socialRTATestServer records RTA requests and controls unsubscribe errors.
type socialRTATestServer struct {
	subscribeCount    atomic.Uint32
	unsubscribeCount  atomic.Uint32
	unsubscribeStatus atomic.Int32
}

// newSocialRTATestClient connects the real RTA client to a local test server.
func newSocialRTATestClient(t *testing.T) (*Client, *socialRTATestServer) {
	t.Helper()
	srv := &socialRTATestServer{}
	server := httptest.NewServer(http.HandlerFunc(srv.handle))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	httpClient := &http.Client{Transport: socialRTATestTransport{target: target, base: transport}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := rta.Dial(ctx, httpClient, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return New(httpClient, conn, xsts.UserInfo{XUID: "1"}, log), srv
}

// handle answers subscribe and unsubscribe requests.
func (s *socialRTATestServer) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{r.Header.Get("Sec-WebSocket-Protocol")},
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	for {
		var request []json.RawMessage
		if err := wsjson.Read(r.Context(), conn, &request); err != nil || len(request) < 2 {
			return
		}
		var typ, seq uint32
		if json.Unmarshal(request[0], &typ) != nil || json.Unmarshal(request[1], &seq) != nil {
			return
		}
		var response []any
		switch typ {
		case 1: // RTA subscribe.
			response = []any{typ, seq, rta.StatusOK, s.subscribeCount.Add(1), map[string]any{}}
		case 2: // RTA unsubscribe.
			s.unsubscribeCount.Add(1)
			response = []any{typ, seq, s.unsubscribeStatus.Load()}
		default:
			return
		}
		if err := wsjson.Write(r.Context(), conn, response); err != nil {
			return
		}
	}
}

// socialRTATestTransport routes the Xbox WebSocket handshake to the local server.
type socialRTATestTransport struct {
	target *url.URL
	base   http.RoundTripper
}

// RoundTrip redirects the request to the test server.
func (t socialRTATestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	r.Host = t.target.Host
	return t.base.RoundTrip(r)
}
