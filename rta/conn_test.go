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
		done <- conn.SubscribeSubscription(ctx, sub)
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
}

func TestDialerCompatibility(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := (Dialer{Options: &websocket.DialOptions{}}).DialContext(ctx, http.DefaultClient)
	if err != nil {
		t.Fatalf("DialContext returned error: %v", err)
	}
	defer conn.Close()
}

func TestReconnectBoundsEachDialAttempt(t *testing.T) {
	requests := make(chan struct{}, maxReconnectAttempts)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
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

	oldTimeout, oldBackoff := reconnectAttemptTimeout, reconnectBackoff
	reconnectAttemptTimeout = 20 * time.Millisecond
	reconnectBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() {
		reconnectAttemptTimeout = oldTimeout
		reconnectBackoff = oldBackoff
	})

	d := Dialer{ErrorLog: slog.New(slog.NewTextHandler(io.Discard, nil))}.newDialer(srv.Client())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = d.reconnect(ctx)
	if err == nil {
		t.Fatal("reconnect returned nil error, want failure")
	}
	for i := 0; i < maxReconnectAttempts; i++ {
		select {
		case <-requests:
		case <-time.After(time.Second):
			t.Fatalf("reconnect made %d dial attempts, want %d", i, maxReconnectAttempts)
		}
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
	err := conn.SubscribeSubscription(ctx, sub)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Subscribe error = %v, want %v", err, wantErr)
	}
	if got := srv.subscribeCount.Load(); got != 1 {
		t.Fatalf("subscribe count = %d, want 1", got)
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
	if err := conn.SubscribeSubscription(ctx, sub); err != nil {
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
	_, tracked := conn.subscriptions[sub.id()]
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

func TestUnsubscribeIDOnlySubscriptionCompatibility(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()
	srv.validateUnsubscribeIDs()

	conn := srv.Dial(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, err := conn.Subscribe(ctx, "test-resource")
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if err := conn.Unsubscribe(ctx, &Subscription{ID: sub.ID}); err != nil {
		t.Fatalf("Unsubscribe returned error: %v", err)
	}
	if got := srv.unsubscribeCount.Load(); got != 1 {
		t.Fatalf("unsubscribe count = %d, want 1", got)
	}
	if sub.Active() {
		t.Fatal("original subscription is still active after ID-only unsubscribe")
	}
}

func TestUnsubscribeInterruptedResponseCompletesLocally(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()
	srv.validateUnsubscribeIDs()

	conn := srv.Dial(t)
	defer conn.Close()

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.SubscribeSubscription(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	srv.closeUnsubscribeResponse()
	if err := conn.Unsubscribe(ctx, sub); err != nil {
		t.Fatalf("Unsubscribe returned error: %v", err)
	}
	if got := srv.unsubscribeCount.Load(); got != 1 {
		t.Fatalf("unsubscribe count = %d, want 1", got)
	}
	if sub.Active() {
		t.Fatal("subscription is still active after interrupted unsubscribe")
	}
	conn.subscriptionsMu.RLock()
	_, tracked := conn.subscriptions[sub.id()]
	conn.subscriptionsMu.RUnlock()
	if tracked {
		t.Fatal("subscription is still tracked after interrupted unsubscribe")
	}
}

func TestUnsubscribeIDOnlyInterruptedResponseCompletesLocally(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()
	srv.validateUnsubscribeIDs()

	conn := srv.Dial(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, err := conn.Subscribe(ctx, "test-resource")
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	srv.closeUnsubscribeResponse()
	if err := conn.Unsubscribe(ctx, &Subscription{ID: sub.ID}); err != nil {
		t.Fatalf("Unsubscribe returned error: %v", err)
	}
	if got := srv.unsubscribeCount.Load(); got != 1 {
		t.Fatalf("unsubscribe count = %d, want 1", got)
	}
	if sub.Active() {
		t.Fatal("original subscription is still active after interrupted ID-only unsubscribe")
	}
	conn.subscriptionsMu.RLock()
	_, tracked := conn.subscriptions[sub.id()]
	conn.subscriptionsMu.RUnlock()
	if tracked {
		t.Fatal("subscription is still tracked after interrupted ID-only unsubscribe")
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
			errs <- conn.SubscribeSubscription(ctx, sub)
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

func TestSubscribeResourceURICompatibility(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sub, err := conn.Subscribe(ctx, "test-resource")
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if sub.ID == 0 {
		t.Fatal("subscription ID was not set")
	}
	if string(sub.Custom) != `{"ok":true}` {
		t.Fatalf("subscription custom = %s, want {\"ok\":true}", sub.Custom)
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
	if err := conn.SubscribeSubscription(ctx, sub); err != nil {
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
	_, tracked := conn.subscriptions[sub.id()]
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
	if err := conn.SubscribeSubscription(ctx, sub); err != nil {
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
	if got, want := srv.subscribeCount.Load(), uint32(1+maxReconnectAttempts); got != want {
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
	subscribeHandler, ok := handler.(SubscriptionSubscribeHandler)
	if !ok {
		t.Fatal("default handler does not implement SubscriptionSubscribeHandler")
	}
	if err := subscribeHandler.HandleSubscribe(json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("HandleSubscribe returned error: %v", err)
	}
	handler.HandleEvent(json.RawMessage(`{"ok":true}`))
	resyncHandler, ok := handler.(SubscriptionResyncHandler)
	if !ok {
		t.Fatal("default handler does not implement SubscriptionResyncHandler")
	}
	resyncHandler.HandleResync()
	errorHandler, ok := handler.(SubscriptionErrorHandler)
	if !ok {
		t.Fatal("default handler does not implement SubscriptionErrorHandler")
	}
	errorHandler.HandleError(errors.New("lost"))
}

func TestResyncMessageRoutesToActiveSubscriptions(t *testing.T) {
	conn := &Conn{
		log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		subscriptions: make(map[uint32]*Subscription),
	}
	conn.ctx, conn.cancel = context.WithCancelCause(context.Background())
	defer conn.cancel(nil)

	activeHandler := &resyncRecordingHandler{resync: make(chan struct{}, 1)}
	active := NewSubscription("active-resource", activeHandler)
	active.activate(1, nil)
	conn.trackSubscription(active)

	inactiveHandler := &resyncRecordingHandler{resync: make(chan struct{}, 1)}
	inactive := NewSubscription("inactive-resource", inactiveHandler)
	inactive.activate(2, nil)
	inactive.deactivate(nil)
	conn.trackSubscription(inactive)

	if err := conn.handleMessage(nil, typeResync, nil); err != nil {
		t.Fatalf("handleMessage returned error: %v", err)
	}
	select {
	case <-activeHandler.resync:
	case <-time.After(time.Second):
		t.Fatal("active subscription did not receive resync")
	}
	select {
	case <-inactiveHandler.resync:
		t.Fatal("inactive subscription received resync")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSubscribeWaitsForReconnectBeforeActiveShortcut(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	handler := newBlockingSubscribeHandler(2)
	sub := NewSubscription("test-resource", handler)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.SubscribeSubscription(ctx, sub); err != nil {
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

	callDone := make(chan error, 1)
	go func() {
		callDone <- conn.SubscribeSubscription(ctx, sub)
	}()

	select {
	case <-callDone:
		t.Fatal("Subscribe returned before reconnect completed")
	case <-time.After(50 * time.Millisecond):
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
	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("Subscribe returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not return after reconnect completed")
	}
}

func TestSubscribeWaitsWhenReconnectDoneNotYetPublished(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.SubscribeSubscription(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	conn.reconnecting.Store(true)
	callDone := make(chan error, 1)
	go func() {
		callDone <- conn.SubscribeSubscription(ctx, sub)
	}()

	select {
	case <-callDone:
		t.Fatal("Subscribe returned while reconnecting was true and reconnectDone was nil")
	case <-time.After(50 * time.Millisecond):
	}

	done := make(chan struct{})
	conn.reconnectMu.Lock()
	conn.reconnectDone = done
	conn.reconnectMu.Unlock()
	select {
	case <-callDone:
		t.Fatal("Subscribe returned before reconnectDone closed")
	case <-time.After(50 * time.Millisecond):
	}

	close(done)
	conn.reconnecting.Store(false)
	conn.reconnectMu.Lock()
	conn.reconnectDone = nil
	conn.reconnectMu.Unlock()
	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("Subscribe returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not return after reconnectDone closed")
	}
}

func TestRequestReconnectPublishesWaitBarrierBeforeReturning(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.SubscribeSubscription(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	dialStarted := make(chan struct{})
	releaseDial := make(chan struct{})
	var dialOnce sync.Once
	blockingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dialOnce.Do(func() { close(dialStarted) })
		select {
		case <-releaseDial:
		case <-r.Context().Done():
		}
	}))
	defer blockingServer.Close()

	u, err := url.Parse(blockingServer.URL)
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

	conn.requestReconnect()
	select {
	case <-dialStarted:
	case <-time.After(time.Second):
		t.Fatal("reconnect dial did not start")
	}

	callCtx, callCancel := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() {
		callDone <- conn.SubscribeSubscription(callCtx, sub)
	}()

	select {
	case err := <-callDone:
		t.Fatalf("Subscribe returned before reconnect completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	callCancel()
	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Subscribe error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not return after context cancellation")
	}
	close(releaseDial)
}

func TestReconnectIncludesSubscribeHandlingCustomPayload(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	handler := newBlockingSubscribeHandler(1)
	sub := NewSubscription("test-resource", handler)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	subscribeDone := make(chan error, 1)
	go func() {
		subscribeDone <- conn.SubscribeSubscription(ctx, sub)
	}()
	select {
	case <-handler.entered:
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not reach subscription handler")
	}

	reconnectDone := make(chan struct{})
	go func() {
		conn.reconnect()
		close(reconnectDone)
	}()

	close(handler.unblock)
	select {
	case err := <-subscribeDone:
		if err != nil {
			t.Fatalf("Subscribe returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not complete")
	}
	select {
	case <-reconnectDone:
	case <-time.After(time.Second):
		t.Fatal("reconnect did not complete")
	}
	if got := srv.subscribeCount.Load(); got != 2 {
		t.Fatalf("subscribe count = %d, want 2", got)
	}
	if !sub.Active() {
		t.Fatal("subscription became inactive after reconnect")
	}
	conn.subscriptionsMu.RLock()
	_, tracked := conn.subscriptions[sub.id()]
	conn.subscriptionsMu.RUnlock()
	if !tracked {
		t.Fatal("subscription was not tracked after reconnect")
	}
}

func TestSubscribeRoutesEventsBeforeSubscribeHandlerReturns(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()
	srv.sendEventAfterSubscribe(1)

	conn := srv.Dial(t)
	defer conn.Close()

	handler := newBlockingEventHandler(1)
	sub := NewSubscription("test-resource", handler)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	subscribeDone := make(chan error, 1)
	go func() {
		subscribeDone <- conn.SubscribeSubscription(ctx, sub)
	}()
	select {
	case <-handler.entered:
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not reach subscription handler")
	}

	close(srv.sendSubscribeEvent)
	select {
	case got := <-handler.events:
		if string(got) != `{"event":true}` {
			t.Fatalf("event payload = %s, want {\"event\":true}", got)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not routed while subscribe handler was blocked")
	}
	close(handler.unblock)
	select {
	case err := <-subscribeDone:
		if err != nil {
			t.Fatalf("Subscribe returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not complete")
	}
}

func TestResubscribeRoutesEventsBeforeSubscribeHandlerReturns(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()
	srv.sendEventAfterSubscribe(2)

	conn := srv.Dial(t)
	defer conn.Close()

	handler := newBlockingEventHandler(2)
	sub := NewSubscription("test-resource", handler)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.SubscribeSubscription(ctx, sub); err != nil {
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

	close(srv.sendSubscribeEvent)
	select {
	case got := <-handler.events:
		if string(got) != `{"event":true}` {
			t.Fatalf("event payload = %s, want {\"event\":true}", got)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not routed while subscribe handler was blocked")
	}
	close(handler.unblock)
	select {
	case <-reconnectDone:
	case <-time.After(time.Second):
		t.Fatal("reconnect did not complete")
	}
}

func TestReconnectRestartsWhenCurrentConnFailsDuringResubscribe(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	handler := newBlockingSubscribeHandler(2)
	sub := NewSubscription("test-resource", handler)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.SubscribeSubscription(ctx, sub); err != nil {
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

	if err := conn.currentConn().Close(websocket.StatusGoingAway, "test close"); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	deadline := time.After(time.Second)
	for !conn.reconnectRequested.Load() {
		select {
		case <-deadline:
			t.Fatal("reconnect was not requested after current connection closed")
		case <-time.After(time.Millisecond):
		}
	}
	close(handler.unblock)
	select {
	case <-reconnectDone:
	case <-time.After(time.Second):
		t.Fatal("reconnect did not complete")
	}
	if got := srv.subscribeCount.Load(); got != 3 {
		t.Fatalf("subscribe count = %d, want 3", got)
	}
	if !sub.Active() {
		t.Fatal("subscription became inactive after queued reconnect")
	}
}

func TestReconnectClosesPreviousCurrentConn(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.SubscribeSubscription(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if got := srv.activeConns.Load(); got != 1 {
		t.Fatalf("active connections = %d, want 1", got)
	}

	oldConn := conn.currentConn()
	conn.reconnect()
	if conn.currentConn() == oldConn {
		t.Fatal("reconnect did not replace current connection")
	}
	deadline := time.After(time.Second)
	for srv.activeConns.Load() != 1 {
		select {
		case <-deadline:
			t.Fatalf("active connections = %d, want 1", srv.activeConns.Load())
		case <-time.After(time.Millisecond):
		}
	}
	if got := srv.subscribeCount.Load(); got != 2 {
		t.Fatalf("subscribe count = %d, want 2", got)
	}
}

func TestReplaceConnRefusesAfterClose(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	newConn, _, err := websocket.Dial(ctx, connectURLString(), &websocket.DialOptions{
		Subprotocols: []string{subprotocol},
		HTTPClient:   http.DefaultClient,
	})
	if err != nil {
		t.Fatalf("Dial returned error: %v", err)
	}

	oldConn := conn.currentConn()
	if err := conn.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if conn.replaceConn(newConn) {
		t.Fatal("replaceConn published a socket after Close")
	}
	if conn.currentConn() != oldConn {
		t.Fatal("replaceConn changed current connection after Close")
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

type blockingEventHandler struct {
	NopSubscriptionHandler
	blockAt int32
	calls   atomic.Int32
	entered chan struct{}
	unblock chan struct{}
	events  chan json.RawMessage
}

func newBlockingEventHandler(blockAt int32) *blockingEventHandler {
	return &blockingEventHandler{
		blockAt: blockAt,
		entered: make(chan struct{}),
		unblock: make(chan struct{}),
		events:  make(chan json.RawMessage, 1),
	}
}

func (h *blockingEventHandler) HandleSubscribe(json.RawMessage) error {
	if h.calls.Add(1) == h.blockAt {
		close(h.entered)
		<-h.unblock
	}
	return nil
}

func (h *blockingEventHandler) HandleEvent(custom json.RawMessage) {
	h.events <- custom
}

type resyncRecordingHandler struct {
	NopSubscriptionHandler
	resync chan struct{}
}

func (h *resyncRecordingHandler) HandleResync() {
	h.resync <- struct{}{}
}

var _ SubscriptionHandler = eventOnlyHandler{}

type eventOnlyHandler struct{}

func (eventOnlyHandler) HandleEvent(json.RawMessage) {}

type connTestServer struct {
	server             *httptest.Server
	subscribeCount     atomic.Uint32
	unsubscribeCount   atomic.Uint32
	unsubscribeStatus  atomic.Int32
	closeSubscribeID   atomic.Uint32
	closeSubscribeMin  atomic.Uint32
	closeUnsubscribe   atomic.Bool
	validateUnsubID    atomic.Bool
	eventSubscribeID   atomic.Uint32
	sendSubscribeEvent chan struct{}
	activeConns        atomic.Int32
}

func newConnTestServer(t *testing.T) *connTestServer {
	t.Helper()
	s := &connTestServer{
		sendSubscribeEvent: make(chan struct{}),
	}
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

func (s *connTestServer) validateUnsubscribeIDs() {
	s.validateUnsubID.Store(true)
}

func (s *connTestServer) sendEventAfterSubscribe(id uint32) {
	s.eventSubscribeID.Store(id)
}

func (s *connTestServer) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{subprotocol},
	})
	if err != nil {
		return
	}
	s.activeConns.Add(1)
	defer func() {
		s.activeConns.Add(-1)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}()
	activeSubscriptions := make(map[uint32]struct{})

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
			activeSubscriptions[id] = struct{}{}
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
			if s.eventSubscribeID.Load() == id {
				select {
				case <-s.sendSubscribeEvent:
				case <-r.Context().Done():
					return
				}
				if err := wsjson.Write(context.Background(), conn, []any{
					typeEvent,
					id,
					json.RawMessage(`{"event":true}`),
				}); err != nil {
					return
				}
			}
		case typeUnsubscribe:
			s.unsubscribeCount.Add(1)
			if s.closeUnsubscribe.Swap(false) {
				_ = conn.Close(websocket.StatusGoingAway, "test close")
				return
			}
			if s.validateUnsubID.Load() {
				if len(payload) < 3 {
					return
				}
				var id uint32
				if err := json.Unmarshal(payload[2], &id); err != nil {
					return
				}
				if _, ok := activeSubscriptions[id]; !ok {
					if err := wsjson.Write(context.Background(), conn, []any{
						typeUnsubscribe,
						seq,
						StatusUnknownResource,
					}); err != nil {
						return
					}
					continue
				}
				delete(activeSubscriptions, id)
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
