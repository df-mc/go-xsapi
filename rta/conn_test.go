package rta

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestSubscribeHandlerErrorDoesNotDeadlock(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	wantErr := errors.New("bad custom payload")
	sub := NewSubscription("test-resource", failingSubscribeHandler{err: wantErr})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- conn.Subscribe(ctx, sub)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Subscribe error = %v, want %v", err, wantErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe deadlocked after HandleSubscribe error")
	}
	if got := srv.unsubscribeCount.Load(); got != 1 {
		t.Fatalf("unsubscribe count = %d, want 1", got)
	}
	if sub.Active() {
		t.Fatal("subscription is active after HandleSubscribe error")
	}
}

func TestSubscribeHandlerErrorWithInterruptedCleanupDoesNotRetry(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	wantErr := errors.New("bad custom payload")
	sub := NewSubscription("test-resource", failingSubscribeHandler{err: wantErr})
	srv.closeUnsubscribeResponse()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := conn.Subscribe(ctx, sub)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Subscribe error = %v, want %v", err, wantErr)
	}
	if got := srv.subscribeCount.Load(); got != 1 {
		t.Fatalf("subscribe count = %d, want 1", got)
	}
	if sub.Active() {
		t.Fatal("subscription is active after HandleSubscribe error")
	}
}

func TestUnsubscribeFailurePreservesTrackedSubscription(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	srv.unsubscribeStatus.Store(StatusServiceUnavailable)
	if err := conn.Unsubscribe(ctx, sub); err == nil {
		t.Fatal("Unsubscribe returned nil error, want failure")
	}
	if !sub.Active() {
		t.Fatal("subscription became inactive after failed unsubscribe")
	}
	conn.subscriptionsMu.RLock()
	_, tracked := conn.subscriptions[sub.ID()]
	conn.subscriptionsMu.RUnlock()
	if !tracked {
		t.Fatal("subscription was untracked after failed unsubscribe")
	}

	srv.unsubscribeStatus.Store(StatusOK)
	if err := conn.Unsubscribe(ctx, sub); err != nil {
		t.Fatalf("retry Unsubscribe returned error: %v", err)
	}
	if sub.Active() {
		t.Fatal("subscription is still active after successful unsubscribe")
	}
}

// TestUnsubscribeLastSubscriptionClosesSocketButKeepsConnReusable verifies that
// idle RTA sockets close without making the Conn unusable for future subscribe calls.
func TestUnsubscribeLastSubscriptionClosesSocketButKeepsConnReusable(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if err := conn.Unsubscribe(ctx, sub); err != nil {
		t.Fatalf("Unsubscribe returned error: %v", err)
	}
	waitAtomicUint32(t, &srv.closeCount, 1, "websocket close count")
	if err := context.Cause(conn.ctx); err != nil {
		t.Fatalf("connection cause = %v, want nil", err)
	}

	next := NewSubscription("next-resource", NopSubscriptionHandler{})
	if err := conn.Subscribe(ctx, next); err != nil {
		t.Fatalf("second Subscribe returned error: %v", err)
	}
	if got := srv.dialCount.Load(); got != 2 {
		t.Fatalf("dial count = %d, want 2", got)
	}
	if !next.Active() {
		t.Fatal("second subscription is inactive")
	}
}

// TestUnsubscribeLastSubscriptionDoesNotCloseSocketDuringSubscribe verifies that
// idle-close does not race with a new subscription that has not been tracked yet.
func TestUnsubscribeLastSubscriptionDoesNotCloseSocketDuringSubscribe(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	blockingHandler := newBlockingSubscribeHandler(1)
	next := NewSubscription("next-resource", blockingHandler)
	subscribeErr := make(chan error, 1)
	go func() {
		subscribeErr <- conn.Subscribe(ctx, next)
	}()
	select {
	case <-blockingHandler.entered:
	case <-time.After(time.Second):
		t.Fatal("second Subscribe did not reach handler")
	}

	unsubscribeErr := make(chan error, 1)
	go func() {
		unsubscribeErr <- conn.Unsubscribe(ctx, sub)
	}()

	select {
	case err := <-unsubscribeErr:
		t.Fatalf("Unsubscribe completed while Subscribe was still in progress: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if got := srv.closeCount.Load(); got != 0 {
		t.Fatalf("websocket close count = %d, want 0 before second Subscribe completes", got)
	}

	close(blockingHandler.unblock)
	if err := <-subscribeErr; err != nil {
		t.Fatalf("second Subscribe returned error: %v", err)
	}
	if err := <-unsubscribeErr; err != nil {
		t.Fatalf("Unsubscribe returned error: %v", err)
	}
	if got := srv.closeCount.Load(); got != 0 {
		t.Fatalf("websocket close count = %d, want 0 while second subscription is active", got)
	}
	if !next.Active() {
		t.Fatal("second subscription is inactive")
	}
}

// TestReconnectWithNoSubscriptionsDoesNotDial verifies that reconnect returns
// before dialing when there are no active subscriptions to restore.
func TestReconnectWithNoSubscriptionsDoesNotDial(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if err := conn.Unsubscribe(ctx, sub); err != nil {
		t.Fatalf("Unsubscribe returned error: %v", err)
	}
	waitAtomicUint32(t, &srv.closeCount, 1, "websocket close count")

	conn.reconnect()
	if got := srv.dialCount.Load(); got != 1 {
		t.Fatalf("dial count = %d, want 1", got)
	}
	if err := context.Cause(conn.ctx); err != nil {
		t.Fatalf("connection cause = %v, want nil", err)
	}
}

func TestConcurrentSubscribeCoalescesSingleHandshake(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			errs <- conn.Subscribe(ctx, sub)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Subscribe returned error: %v", err)
		}
	}
	if got := srv.subscribeCount.Load(); got != 1 {
		t.Fatalf("subscribe count = %d, want 1", got)
	}
}

// TestPopSubscriptionsPreservesSkippedActiveSubscriptions verifies that reconnect
// keeps skipped active subscriptions available for close-time error notification.
func TestPopSubscriptionsPreservesSkippedActiveSubscriptions(t *testing.T) {
	conn := &Conn{
		subscriptions: make(map[uint32]*Subscription),
	}

	keep := NewSubscription("keep-resource", NopSubscriptionHandler{})
	keep.activate(1, nil)
	skip := NewSubscription("skip-resource", NopSubscriptionHandler{})
	skip.activate(2, nil)
	skip.setUnsubscribing(true)
	conn.subscriptions[keep.ID()] = keep
	conn.subscriptions[skip.ID()] = skip

	resubscribe, popped := conn.popSubscriptions()
	if got, want := len(resubscribe), 1; got != want {
		t.Fatalf("resubscribe count = %d, want %d", got, want)
	}
	if resubscribe[0] != keep {
		t.Fatal("resubscribe list did not contain keep subscription")
	}
	if got, want := len(popped), 2; got != want {
		t.Fatalf("popped count = %d, want %d", got, want)
	}
	if len(conn.subscriptions) != 0 {
		t.Fatalf("subscriptions map length = %d, want 0", len(conn.subscriptions))
	}
}

func TestReconnectRetriesInterruptedResubscribe(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	srv.closeSubscribe(2)
	done := make(chan struct{})
	go func() {
		conn.reconnect()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect did not retry interrupted resubscribe")
	}
	if got := srv.subscribeCount.Load(); got != 3 {
		t.Fatalf("subscribe count = %d, want 3", got)
	}
	if !sub.Active() {
		t.Fatal("subscription became inactive after interrupted resubscribe")
	}
	conn.subscriptionsMu.RLock()
	_, tracked := conn.subscriptions[sub.ID()]
	conn.subscriptionsMu.RUnlock()
	if !tracked {
		t.Fatal("subscription was not tracked after retried resubscribe")
	}
}

func TestReconnectClosesAfterPersistentInterruptedResubscribe(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	srv.closeSubscribesFrom(2)
	done := make(chan struct{})
	go func() {
		conn.reconnect()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect did not finish after persistent interrupted resubscribe")
	}
	if got, want := srv.subscribeCount.Load(), uint32(1+maxResubscribeAttempts); got != want {
		t.Fatalf("subscribe count = %d, want %d", got, want)
	}
	if err := context.Cause(conn.ctx); err == nil || !strings.Contains(err.Error(), "resubscribe interrupted") {
		t.Fatalf("connection cause = %v, want resubscribe interrupted", err)
	}
	if sub.Active() {
		t.Fatal("subscription is still active after reconnect failure")
	}
}

func TestZeroValueSubscriptionUsesNopHandler(t *testing.T) {
	var sub Subscription

	handler := sub.handler()
	if err := handler.HandleSubscribe(json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("HandleSubscribe returned error: %v", err)
	}
	handler.HandleEvent(json.RawMessage(`{"ok":true}`))
	handler.HandleResync()
	handler.HandleError(errors.New("lost"))
}

func TestSubscribeCanUseReconnectedConnDuringResubscribe(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	handler := newBlockingSubscribeHandler(2)
	sub := NewSubscription("test-resource", handler)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	reconnectDone := make(chan struct{})
	go func() {
		conn.reconnect()
		close(reconnectDone)
	}()
	select {
	case <-handler.entered:
	case <-time.After(time.Second):
		t.Fatal("resubscribe did not reach subscription handler")
	}

	newSub := NewSubscription("new-resource", NopSubscriptionHandler{})
	callDone := make(chan error, 1)
	go func() {
		callDone <- conn.Subscribe(ctx, newSub)
	}()

	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("Subscribe returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe blocked behind in-progress resubscribe")
	}

	close(handler.unblock)
	select {
	case <-reconnectDone:
	case <-time.After(time.Second):
		t.Fatal("reconnect did not complete")
	}
	if !sub.Active() {
		t.Fatal("existing subscription became inactive after resubscribe")
	}
	if !newSub.Active() {
		t.Fatal("new subscription is inactive")
	}
}

type failingSubscribeHandler struct {
	NopSubscriptionHandler
	err error
}

func (h failingSubscribeHandler) HandleSubscribe(json.RawMessage) error {
	return h.err
}

type blockingSubscribeHandler struct {
	NopSubscriptionHandler
	blockAt int32
	calls   atomic.Int32
	entered chan struct{}
	unblock chan struct{}
}

func newBlockingSubscribeHandler(blockAt int32) *blockingSubscribeHandler {
	return &blockingSubscribeHandler{
		blockAt: blockAt,
		entered: make(chan struct{}),
		unblock: make(chan struct{}),
	}
}

func (h *blockingSubscribeHandler) HandleSubscribe(json.RawMessage) error {
	if h.calls.Add(1) == h.blockAt {
		close(h.entered)
		<-h.unblock
	}
	return nil
}

type connTestServer struct {
	server            *httptest.Server
	dialCount         atomic.Uint32
	closeCount        atomic.Uint32
	subscribeCount    atomic.Uint32
	unsubscribeCount  atomic.Uint32
	unsubscribeStatus atomic.Int32
	closeSubscribeID  atomic.Uint32
	closeSubscribeMin atomic.Uint32
	closeUnsubscribe  atomic.Bool
}

func newConnTestServer(t *testing.T) *connTestServer {
	t.Helper()
	s := &connTestServer{}
	s.unsubscribeStatus.Store(StatusOK)
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))

	u, err := url.Parse(s.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	connectURLMu.Lock()
	oldURL := connectURL
	connectURL = &url.URL{Scheme: "ws", Host: u.Host, Path: "/"}
	connectURLMu.Unlock()
	t.Cleanup(func() {
		connectURLMu.Lock()
		connectURL = oldURL
		connectURLMu.Unlock()
	})
	return s
}

func (s *connTestServer) Close() {
	s.server.Close()
}

func (s *connTestServer) Dial(t *testing.T) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	conn, err := Dial(ctx, http.DefaultClient, log)
	if err != nil {
		t.Fatalf("Dial returned error: %v", err)
	}
	return conn
}

func (s *connTestServer) closeSubscribe(id uint32) {
	s.closeSubscribeID.Store(id)
}

func (s *connTestServer) closeSubscribesFrom(id uint32) {
	s.closeSubscribeMin.Store(id)
}

func (s *connTestServer) closeUnsubscribeResponse() {
	s.closeUnsubscribe.Store(true)
}

func (s *connTestServer) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{subprotocol},
	})
	if err != nil {
		return
	}
	s.dialCount.Add(1)
	defer func() {
		s.closeCount.Add(1)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()

	for {
		var payload []json.RawMessage
		if err := wsjson.Read(context.Background(), conn, &payload); err != nil {
			return
		}
		typ, err := readHeader(payload)
		if err != nil {
			return
		}
		if len(payload) < 2 {
			return
		}
		var seq uint32
		if err := json.Unmarshal(payload[1], &seq); err != nil {
			return
		}

		switch typ {
		case typeSubscribe:
			id := s.subscribeCount.Add(1)
			if minID := s.closeSubscribeMin.Load(); minID != 0 && id >= minID {
				_ = conn.Close(websocket.StatusGoingAway, "test close")
				return
			}
			if s.closeSubscribeID.Load() == id {
				_ = conn.Close(websocket.StatusGoingAway, "test close")
				return
			}
			if err := wsjson.Write(context.Background(), conn, []any{
				typeSubscribe,
				seq,
				StatusOK,
				id,
				json.RawMessage(`{"ok":true}`),
			}); err != nil {
				return
			}
		case typeUnsubscribe:
			s.unsubscribeCount.Add(1)
			if s.closeUnsubscribe.Load() {
				_ = conn.Close(websocket.StatusGoingAway, "test close")
				return
			}
			if err := wsjson.Write(context.Background(), conn, []any{
				typeUnsubscribe,
				seq,
				s.unsubscribeStatus.Load(),
			}); err != nil {
				return
			}
		default:
			return
		}
	}
}

// waitAtomicUint32 waits until counter reaches want or fails the test.
func waitAtomicUint32(t *testing.T, counter *atomic.Uint32, want uint32, name string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		got := counter.Load()
		if got >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s = %d, want at least %d", name, got, want)
		case <-tick.C:
		}
	}
}
