package rta

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v7"
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

// TestUnsubscribeLastSubscriptionKeepsSocketReusable verifies that removing the
// last subscription does not proactively close the WebSocket. The open socket
// remains available for a later Subscribe to avoid an unnecessary redial.
func TestUnsubscribeLastSubscriptionKeepsSocketReusable(t *testing.T) {
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
	assertAtomicUint32Stays(t, &srv.closeCount, 0, "websocket close count")
	if err := context.Cause(conn.ctx); err != nil {
		t.Fatalf("connection cause = %v, want nil", err)
	}

	next := NewSubscription("next-resource", NopSubscriptionHandler{})
	if err := conn.Subscribe(ctx, next); err != nil {
		t.Fatalf("second Subscribe returned error: %v", err)
	}
	if got := srv.dialCount.Load(); got != 1 {
		t.Fatalf("dial count = %d, want 1", got)
	}
	if !next.Active() {
		t.Fatal("second subscription is inactive")
	}
}

// TestUnsubscribeWaitsForInFlightSubscribe verifies that unsubscribe cannot
// remove the last tracked subscription while another Subscribe is still running.
func TestUnsubscribeWaitsForInFlightSubscribe(t *testing.T) {
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
// before dialing when Xbox closes an idle WebSocket with no active subscriptions
// to restore.
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
	srv.closeAfterUnsubscribeResponse()
	if err := conn.Unsubscribe(ctx, sub); err != nil {
		t.Fatalf("Unsubscribe returned error: %v", err)
	}
	waitAtomicUint32(t, &srv.closeCount, 1, "websocket close count")
	assertAtomicUint32Stays(t, &srv.dialCount, 1, "dial count")
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

// TestTakeSubscriptionsForReconnectSkipsUnsubscribing verifies that reconnect
// only takes subscriptions that still need to be restored on a new WebSocket.
func TestTakeSubscriptionsForReconnectSkipsUnsubscribing(t *testing.T) {
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

	resubscribe := conn.takeSubscriptionsForReconnect()
	if got, want := len(resubscribe), 1; got != want {
		t.Fatalf("resubscribe count = %d, want %d", got, want)
	}
	if resubscribe[0] != keep {
		t.Fatal("resubscribe list did not contain keep subscription")
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

// A socket that keeps dropping mid-handshake must not turn into a closed Conn;
// the reconnect keeps going until a handshake lands.
func TestReconnectOutlastsPersistentInterruptedResubscribe(t *testing.T) {
	shortBackoff(t)
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

	// Well past the handful of rounds a bounded budget would allow.
	waitAtomicUint32(t, &srv.subscribeCount, 8, "subscribe count")
	if conn.ctx.Err() != nil {
		t.Fatalf("connection closed during interrupted resubscribes: %v", context.Cause(conn.ctx))
	}
	srv.closeSubscribesFrom(0)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect did not finish once the handshake stopped being interrupted")
	}
	if !sub.Active() {
		t.Fatal("subscription is inactive after the reconnect finally landed")
	}
	conn.subscriptionsMu.RLock()
	_, tracked := conn.subscriptions[sub.ID()]
	conn.subscriptionsMu.RUnlock()
	if !tracked {
		t.Fatal("subscription was not tracked after the reconnect landed")
	}
}

// An outage longer than any fixed dial budget must be waited out, not turned
// into a closed Conn with dead subscriptions.
func TestReconnectKeepsDialingThroughOutage(t *testing.T) {
	shortBackoff(t)
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

	srv.rejectDials.Store(true)
	done := make(chan struct{})
	go func() {
		conn.reconnect()
		close(done)
	}()

	waitAtomicUint32(t, &srv.rejectedDials, 10, "rejected dial count")
	if conn.ctx.Err() != nil {
		t.Fatalf("connection closed during the outage: %v", context.Cause(conn.ctx))
	}
	srv.rejectDials.Store(false)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect did not finish once dials were accepted again")
	}
	if got := srv.subscribeCount.Load(); got != 2 {
		t.Fatalf("subscribe count = %d, want 2 (initial + one resubscribe)", got)
	}
	if !sub.Active() {
		t.Fatal("subscription is inactive after the outage ended")
	}
}

// Closing the Conn during an outage ends the retries and reports the close
// cause to the subscriptions the reconnect was holding.
func TestCloseDuringOutageStopsReconnectAndDeactivates(t *testing.T) {
	shortBackoff(t)
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)

	sub := NewSubscription("test-resource", NopSubscriptionHandler{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	srv.rejectDials.Store(true)
	done := make(chan struct{})
	go func() {
		conn.reconnect()
		close(done)
	}()
	waitAtomicUint32(t, &srv.rejectedDials, 3, "rejected dial count")

	if err := conn.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect kept running after Close")
	}
	if sub.Active() {
		t.Fatal("subscription is still active after Close")
	}
}

// A Close that lands while a resubscribe handshake is in flight must still
// end with the subscription deactivated, even though the handshake re-tracks
// it after Close's own deactivation loop has run.
func TestCloseDuringResubscribeHandshakeDeactivates(t *testing.T) {
	shortBackoff(t)
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	handler := newBlockingSubscribeHandler(2)
	sub := NewSubscription("test-resource", handler)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	done := make(chan struct{})
	go func() {
		conn.reconnect()
		close(done)
	}()
	select {
	case <-handler.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("resubscribe handshake did not reach the handler")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	close(handler.unblock)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect did not finish after Close")
	}
	if sub.Active() {
		t.Fatal("subscription is still active on a closed Conn")
	}
	conn.subscriptionsMu.RLock()
	_, tracked := conn.subscriptions[sub.ID()]
	conn.subscriptionsMu.RUnlock()
	if tracked {
		t.Fatal("subscription is still tracked on a closed Conn")
	}
}

// Close racing a reconnect must never leave a freshly dialed socket open:
// every socket the server accepted is closed once Close has returned.
func TestCloseRacingReconnectLeavesNoOpenSocket(t *testing.T) {
	shortBackoff(t)
	srv := newConnTestServer(t)
	defer srv.Close()

	for range 50 {
		conn := srv.Dial(t)
		sub := NewSubscription("test-resource", NopSubscriptionHandler{})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := conn.Subscribe(ctx, sub); err != nil {
			cancel()
			t.Fatalf("Subscribe returned error: %v", err)
		}
		cancel()
		done := make(chan struct{})
		go func() {
			conn.reconnect()
			close(done)
		}()
		if err := conn.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("reconnect did not finish after Close")
		}
		if sub.Active() {
			t.Fatal("subscription is still active after Close")
		}
	}
	waitAtomicUint32(t, &srv.closeCount, srv.dialCount.Load(), "closed socket count")
}

// TestCloseCancelsLongReconnectBackoff checks that both retry loops allow a
// delay beyond backoff's default elapsed-time limit and stop promptly on Close.
func TestCloseCancelsLongReconnectBackoff(t *testing.T) {
	for _, duringResubscribe := range []bool{false, true} {
		name := "dial"
		if duringResubscribe {
			name = "resubscribe"
		}
		t.Run(name, func(t *testing.T) {
			old := reconnectBackoff
			reconnectBackoff = func() backoff.BackOff { return backoff.NewConstantBackOff(time.Hour) }
			t.Cleanup(func() { reconnectBackoff = old })
			srv := newConnTestServer(t)
			defer srv.Close()
			conn := srv.Dial(t)
			defer conn.Close()
			sub := NewSubscription("test-resource", NopSubscriptionHandler{})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := conn.Subscribe(ctx, sub); err != nil {
				t.Fatal(err)
			}
			if duringResubscribe {
				srv.closeSubscribe(2)
			} else {
				srv.rejectDials.Store(true)
			}
			done := make(chan struct{})
			go func() {
				conn.reconnect()
				close(done)
			}()
			if duringResubscribe {
				waitAtomicUint32(t, &srv.subscribeCount, 2, "subscribe count")
			} else {
				waitAtomicUint32(t, &srv.rejectedDials, 1, "rejected dial count")
			}
			select {
			case <-done:
				t.Fatal("reconnect stopped instead of waiting for Close")
			case <-time.After(100 * time.Millisecond):
			}
			if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("Close did not cancel the reconnect wait")
			}
			if sub.Active() {
				t.Fatal("subscription is active after Close")
			}
		})
	}
}

// A socket that stays open but goes silent must be replaced and its
// subscriptions restored, or MPSD keeps treating the stale connection ID as offline.
func TestSilentWebSocketIsReplaced(t *testing.T) {
	shortBackoff(t)
	shortPing(t, 5*time.Millisecond, 250*time.Millisecond)
	srv := newConnTestServer(t)
	defer srv.Close()
	srv.silentDial.Store(1)

	conn := srv.Dial(t)
	defer conn.Close()

	h := &countingSubscribeHandler{}
	sub := NewSubscription("test-resource", h)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	waitAtomicUint32(t, &srv.dialCount, 2, "dial count")
	waitAtomicUint32(t, &h.subscribes, 2, "HandleSubscribe calls")
	if !sub.Active() {
		t.Fatal("subscription is inactive after the silent socket was replaced")
	}
	if got := sub.ID(); got == 1 {
		t.Fatal("subscription still has its ID from the silent socket")
	}
}

// A replacement socket lost while its resubscribe callback still runs must be
// replaced again instead of staying current with no reader.
func TestReplacementLostDuringResubscribeIsReplaced(t *testing.T) {
	shortBackoff(t)
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	h := newBlockingSubscribeHandler(2)
	t.Cleanup(h.release)
	sub := NewSubscription("test-resource", h)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	srv.closeAfterSubscribeID.Store(2)
	go conn.reconnect()
	select {
	case <-h.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("resubscribe callback did not run")
	}
	waitFor(t, func() bool {
		conn.reconnectMu.Lock()
		defer conn.reconnectMu.Unlock()
		return conn.lostConn != nil && conn.isCurrentConn(conn.lostConn)
	}, "the current socket to be recorded as lost")
	h.release()

	waitAtomicUint32(t, &srv.dialCount, 3, "dial count")
	waitAtomicUint32(t, &srv.subscribeCount, 3, "subscribe count")
	if err := conn.wait(ctx); err != nil {
		t.Fatalf("wait returned error: %v", err)
	}
	if !sub.Active() || sub.ID() != 3 {
		t.Fatalf("subscription active=%v ID=%d, want active on the third socket", sub.Active(), sub.ID())
	}
}

// A socket lost while the first HandleSubscribe runs must not leave the
// subscription active but untracked, where no reconnect restores it.
func TestSubscribeLostDuringHandleSubscribeRetries(t *testing.T) {
	srv := newConnTestServer(t)
	defer srv.Close()
	srv.closeAfterSubscribeID.Store(1)

	conn := srv.Dial(t)
	defer conn.Close()

	h := newBlockingSubscribeHandler(1)
	t.Cleanup(h.release)
	sub := NewSubscription("test-resource", h)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	errs := make(chan error, 1)
	go func() { errs <- conn.Subscribe(ctx, sub) }()
	select {
	case <-h.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleSubscribe did not run")
	}
	// The reconnect found nothing tracked and dropped the socket.
	waitFor(t, func() bool { return conn.isCurrentConn(nil) }, "the lost socket to be dropped")
	h.release()

	if err := <-errs; err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if !sub.Active() || sub.ID() != 2 {
		t.Fatalf("subscription active=%v ID=%d, want active on the second socket", sub.Active(), sub.ID())
	}
	conn.subscriptionsMu.RLock()
	_, tracked := conn.subscriptions[sub.ID()]
	conn.subscriptionsMu.RUnlock()
	if !tracked {
		t.Fatal("subscription is not tracked, so a later reconnect would skip it")
	}
}

// A replacement socket that never acknowledges resubscribes must be replaced
// again, not cost the subscription.
func TestSilentReplacementDuringResubscribeIsReplaced(t *testing.T) {
	shortBackoff(t)
	shortPing(t, time.Hour, 100*time.Millisecond)
	shortResubscribe(t, 250*time.Millisecond)
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	h := &countingErrorHandler{}
	sub := NewSubscription("test-resource", h)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	srv.mutedDial.Store(2)
	srv.silentDial.Store(2)
	reconnectWithin(t, conn)

	if got := srv.dialCount.Load(); got != 3 {
		t.Fatalf("dial count = %d, want 3", got)
	}
	if !sub.Active() || sub.ID() != 2 {
		t.Fatalf("subscription active=%v ID=%d, want active on the third socket", sub.Active(), sub.ID())
	}
	if got := h.errors.Load(); got != 0 {
		t.Fatalf("HandleError calls = %d, want 0", got)
	}
}

// A resubscribe the service ignores on a socket that still answers pings fails
// that subscription instead of reconnecting forever.
func TestIgnoredResubscribeOnLiveSocketIsTerminal(t *testing.T) {
	shortBackoff(t)
	shortPing(t, time.Hour, time.Second)
	shortResubscribe(t, 50*time.Millisecond)
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	h := &countingErrorHandler{}
	sub := NewSubscription("test-resource", h)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	srv.mutedDial.Store(2)
	reconnectWithin(t, conn)

	if got := srv.dialCount.Load(); got != 2 {
		t.Fatalf("dial count = %d, want 2", got)
	}
	if sub.Active() {
		t.Fatal("ignored subscription is still active")
	}
	waitAtomicUint32(t, &h.errors, 1, "HandleError calls")
}

// An interrupted resubscribe must not keep its old ID in the map, where a
// subscription that succeeded on the new socket can reuse it.
func TestInterruptedResubscribeDoesNotShadowNewID(t *testing.T) {
	shortBackoff(t)
	shortPing(t, time.Hour, 100*time.Millisecond)
	shortResubscribe(t, 250*time.Millisecond)
	srv := newConnTestServer(t)
	defer srv.Close()
	srv.perSocketIDs.Store(true)

	conn := srv.Dial(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	slow := NewSubscription("slow", NopSubscriptionHandler{})
	fast := NewSubscription("fast", NopSubscriptionHandler{})
	for _, sub := range []*Subscription{slow, fast} {
		if err := conn.Subscribe(ctx, sub); err != nil {
			t.Fatalf("Subscribe returned error: %v", err)
		}
	}
	if slow.ID() != 1 || fast.ID() != 2 {
		t.Fatalf("IDs on socket 1 = %d, %d, want 1, 2", slow.ID(), fast.ID())
	}

	// Socket 2 answers fast, which can take ID 1, then goes silent on slow.
	srv.mutedResource.Store("slow")
	srv.mutedDial.Store(2)
	srv.silentDial.Store(2)
	reconnectWithin(t, conn)

	if got := srv.dialCount.Load(); got != 3 {
		t.Fatalf("dial count = %d, want 3", got)
	}
	conn.subscriptionsMu.RLock()
	defer conn.subscriptionsMu.RUnlock()
	for _, sub := range []*Subscription{slow, fast} {
		if !sub.Active() || conn.subscriptions[sub.ID()] != sub {
			t.Fatalf("subscription %q active=%v ID=%d is not tracked under its ID", sub.ResourceURI(), sub.Active(), sub.ID())
		}
	}
}

type countingErrorHandler struct {
	NopSubscriptionHandler
	errors atomic.Uint32
}

func (h *countingErrorHandler) HandleError(error) {
	h.errors.Add(1)
}

// A subscription made on a socket that a running reconnect already found lost
// must wait for the replacement, even after that reconnect starts another attempt.
func TestSubscribeOnLostSocketWaitsForLaterAttempt(t *testing.T) {
	shortBackoff(t)
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a := newBlockingSubscribeHandler(2)
	t.Cleanup(a.release)
	subA := NewSubscription("a", a)
	if err := conn.Subscribe(ctx, subA); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	// Socket 2 acknowledges A's resubscribe (ID 2), then X (ID 3), then closes.
	srv.closeAfterSubscribeID.Store(3)
	go conn.reconnect()
	select {
	case <-a.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("A's resubscribe callback did not run")
	}

	x := newBlockingSubscribeHandler(1)
	t.Cleanup(x.release)
	subX := NewSubscription("x", x)
	errs := make(chan error, 1)
	go func() { errs <- conn.Subscribe(ctx, subX) }()
	select {
	case <-x.entered:
	case err := <-errs:
		t.Fatalf("Subscribe returned before its callback ran: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("X's callback did not run")
	}
	waitFor(t, func() bool {
		conn.reconnectMu.Lock()
		defer conn.reconnectMu.Unlock()
		return conn.lostConn != nil && conn.isCurrentConn(conn.lostConn)
	}, "socket 2 to be recorded as lost")

	// The reconnect runs again for socket 2 and holds its dial while X finishes.
	srv.rejectDials.Store(true)
	a.release()
	waitAtomicUint32(t, &srv.rejectedDials, 1, "rejected dials")
	x.release()
	waitFor(t, func() bool {
		conn.subscriptionsMu.RLock()
		defer conn.subscriptionsMu.RUnlock()
		return conn.subscriptions[subX.ID()] == subX || !subX.Active()
	}, "X to be tracked or rejected")
	srv.rejectDials.Store(false)

	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("Subscribe returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return")
	}
	if err := conn.wait(ctx); err != nil {
		t.Fatalf("wait returned error: %v", err)
	}
	if subX.ID() <= 3 {
		t.Fatalf("X ID = %d, want an ID from the socket after socket 2", subX.ID())
	}
	conn.subscriptionsMu.RLock()
	defer conn.subscriptionsMu.RUnlock()
	for _, sub := range []*Subscription{subA, subX} {
		if !sub.Active() || conn.subscriptions[sub.ID()] != sub {
			t.Fatalf("subscription %q active=%v ID=%d is not tracked under its ID", sub.ResourceURI(), sub.Active(), sub.ID())
		}
	}
}

// A subscription removed while the reconnect holds it for a retry must stay removed.
func TestUnsubscribedWhileHeldIsNotRestored(t *testing.T) {
	shortBackoff(t)
	shortPing(t, time.Hour, 100*time.Millisecond)
	shortResubscribe(t, 250*time.Millisecond)
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

	srv.mutedDial.Store(2)
	srv.silentDial.Store(2)
	srv.rejectDialsFrom.Store(3)
	done := make(chan struct{})
	go func() {
		conn.reconnect()
		close(done)
	}()
	// The silent socket interrupted the resubscribe; the reconnect now holds sub.
	waitAtomicUint32(t, &srv.rejectedDials, 1, "rejected dials")
	// What a concurrent Unsubscribe does once it finds the socket closed.
	sub.opMu.Lock()
	sub.setUnsubscribing(true)
	sub.deactivate(ErrUnsubscribed)
	sub.opMu.Unlock()
	subscribes := srv.subscribeCount.Load()
	srv.rejectDialsFrom.Store(0)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconnect did not finish")
	}
	if sub.Active() {
		t.Fatal("unsubscribed subscription was restored")
	}
	if got := srv.subscribeCount.Load(); got != subscribes {
		t.Fatalf("subscribe count = %d, want %d", got, subscribes)
	}
}

// Handshakes that time out together share one liveness ping.
func TestTimedOutResubscribesShareOnePing(t *testing.T) {
	shortBackoff(t)
	shortPing(t, time.Hour, 100*time.Millisecond)
	shortResubscribe(t, 250*time.Millisecond)
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, uri := range []string{"a", "b"} {
		if err := conn.Subscribe(ctx, NewSubscription(uri, NopSubscriptionHandler{})); err != nil {
			t.Fatalf("Subscribe returned error: %v", err)
		}
	}

	srv.mutedDial.Store(2)
	srv.silentDial.Store(2)
	reconnectWithin(t, conn)
	if got := srv.pingCount.Load(); got != 1 {
		t.Fatalf("ping count = %d, want 1", got)
	}
}

// A service that keeps dropping each socket right after its resubscribes
// succeed must be paced by backoff, not redialed at once.
func TestLostReplacementRoundsBackOff(t *testing.T) {
	var waits atomic.Uint32
	old := reconnectBackoff
	reconnectBackoff = func() backoff.BackOff { return countingBackOff{&waits} }
	t.Cleanup(func() { reconnectBackoff = old })
	srv := newConnTestServer(t)
	defer srv.Close()

	conn := srv.Dial(t)
	defer conn.Close()

	h := &lossWaitingHandler{conn: conn, from: 2}
	h.waiting.Store(true)
	sub := NewSubscription("test-resource", h)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Subscribe(ctx, sub); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}

	srv.closeAfterSubscribeMin.Store(2)
	go conn.reconnect()
	waitAtomicUint32(t, &srv.dialCount, 4, "dial count")
	srv.closeAfterSubscribeMin.Store(0)
	if waits.Load() == 0 {
		t.Fatal("reconnect rounds did not back off")
	}
}

// countingBackOff counts waits and returns a millisecond each.
type countingBackOff struct{ calls *atomic.Uint32 }

func (b countingBackOff) NextBackOff() time.Duration {
	b.calls.Add(1)
	return time.Millisecond
}

func (countingBackOff) Reset() {}

// lossWaitingHandler holds each callback from call from on until the socket it
// came on is recorded lost or dropped, so the loss lands before it returns.
type lossWaitingHandler struct {
	NopSubscriptionHandler
	conn    *Conn
	from    int32 // first call that waits
	calls   atomic.Int32
	waiting atomic.Bool
}

func (h *lossWaitingHandler) HandleSubscribe(json.RawMessage) error {
	if h.calls.Add(1) < h.from || !h.waiting.Load() {
		return nil
	}
	socket := h.conn.currentConn()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.conn.reconnectMu.Lock()
		lost := h.conn.lostConn == socket || !h.conn.isCurrentConn(socket)
		h.conn.reconnectMu.Unlock()
		if lost {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

// A first subscribe whose socket keeps closing right after the ACK must retry
// on a backoff schedule, not redial at once while holding subscriptionOpMu.
func TestSubscribeLostAfterEveryAckBacksOff(t *testing.T) {
	var waits atomic.Uint32
	old := reconnectBackoff
	reconnectBackoff = func() backoff.BackOff { return countingBackOff{&waits} }
	t.Cleanup(func() { reconnectBackoff = old })
	srv := newConnTestServer(t)
	defer srv.Close()
	srv.closeAfterSubscribeMin.Store(1)

	conn := srv.Dial(t)
	defer conn.Close()

	h := &lossWaitingHandler{conn: conn, from: 1}
	h.waiting.Store(true)
	sub := NewSubscription("test-resource", h)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errs := make(chan error, 1)
	go func() { errs <- conn.Subscribe(ctx, sub) }()

	waitAtomicUint32(t, &srv.subscribeCount, 3, "subscribe count")
	srv.closeAfterSubscribeMin.Store(0)
	h.waiting.Store(false)
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("Subscribe returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe did not return")
	}
	if waits.Load() == 0 {
		t.Fatal("Subscribe retried without backing off")
	}
}

// A socket that answers pings must stay in use.
func TestRespondingWebSocketIsKept(t *testing.T) {
	// The timeout leaves room for scheduler delays but is short enough that a
	// socket ignoring pings is replaced before five pings arrive.
	shortPing(t, 5*time.Millisecond, 250*time.Millisecond)
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
	waitAtomicUint32(t, &srv.pingCount, 5, "ping count")
	if got := srv.dialCount.Load(); got != 1 {
		t.Fatalf("dial count = %d, want 1", got)
	}
}

// reconnectWithin runs a reconnect and fails the test if it does not finish.
func reconnectWithin(t *testing.T, conn *Conn) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		conn.reconnect()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconnect did not finish")
	}
}

// shortPing makes new dialers ping every interval and give up after timeout.
func shortPing(t *testing.T, interval, timeout time.Duration) {
	t.Helper()
	oldInterval, oldTimeout := pingInterval, pingTimeout
	pingInterval, pingTimeout = interval, timeout
	t.Cleanup(func() { pingInterval, pingTimeout = oldInterval, oldTimeout })
}

// shortResubscribe makes new dialers give up a resubscribe handshake after timeout.
func shortResubscribe(t *testing.T, timeout time.Duration) {
	t.Helper()
	old := resubscribeTimeout
	resubscribeTimeout = timeout
	t.Cleanup(func() { resubscribeTimeout = old })
}

// waitFor waits until cond holds or fails the test.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

type countingSubscribeHandler struct {
	NopSubscriptionHandler
	subscribes atomic.Uint32
}

func (h *countingSubscribeHandler) HandleSubscribe(json.RawMessage) error {
	h.subscribes.Add(1)
	return nil
}

// shortBackoff makes reconnect attempts wait one millisecond for the test.
func shortBackoff(t *testing.T) {
	t.Helper()
	old := reconnectBackoff
	reconnectBackoff = func() backoff.BackOff { return backoff.NewConstantBackOff(time.Millisecond) }
	t.Cleanup(func() { reconnectBackoff = old })
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
	once    sync.Once
}

func newBlockingSubscribeHandler(blockAt int32) *blockingSubscribeHandler {
	return &blockingSubscribeHandler{
		blockAt: blockAt,
		entered: make(chan struct{}),
		unblock: make(chan struct{}),
	}
}

// release unblocks the handler; it is safe to call more than once.
func (h *blockingSubscribeHandler) release() {
	h.once.Do(func() { close(h.unblock) })
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
	closeAfterUnsub   atomic.Bool
	// rejectDials refuses WebSocket upgrades, simulating a service outage, as
	// does rejectDialsFrom for every upgrade attempt from that number on.
	rejectDials     atomic.Bool
	rejectDialsFrom atomic.Uint32
	dialAttempts    atomic.Uint32
	rejectedDials   atomic.Uint32
	// silentDial is the dial number whose socket stays open but never answers pings.
	silentDial atomic.Uint32
	// closeAfterSubscribeID closes the socket right after acknowledging this subscribe,
	// and closeAfterSubscribeMin after acknowledging any subscribe from that ID on.
	closeAfterSubscribeID  atomic.Uint32
	closeAfterSubscribeMin atomic.Uint32
	// mutedDial is the dial number whose socket never acknowledges a subscribe
	// to mutedResource, or to any resource when mutedResource is empty.
	mutedDial     atomic.Uint32
	mutedResource atomic.Value // string
	// perSocketIDs numbers subscriptions from 1 on each socket instead of globally.
	perSocketIDs atomic.Bool
	pingCount    atomic.Uint32
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

func (s *connTestServer) closeAfterUnsubscribeResponse() {
	s.closeAfterUnsub.Store(true)
}

func (s *connTestServer) handle(w http.ResponseWriter, r *http.Request) {
	attempt := s.dialAttempts.Add(1)
	if from := s.rejectDialsFrom.Load(); s.rejectDials.Load() || (from != 0 && attempt >= from) {
		s.rejectedDials.Add(1)
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	var dial uint32
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{subprotocol},
		OnPingReceived: func(context.Context, []byte) bool {
			s.pingCount.Add(1)
			return s.silentDial.Load() != dial
		},
	})
	if err != nil {
		return
	}
	dial = s.dialCount.Add(1)
	var socketIDs uint32
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
			if s.mutedDial.Load() == dial && s.muted(payload) {
				continue
			}
			id := s.subscribeCount.Add(1)
			if s.perSocketIDs.Load() {
				socketIDs++
				id = socketIDs
			}
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
			if minID := s.closeAfterSubscribeMin.Load(); s.closeAfterSubscribeID.Load() == id || (minID != 0 && id >= minID) {
				_ = conn.Close(websocket.StatusGoingAway, "test close")
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
			if s.closeAfterUnsub.Load() {
				_ = conn.Close(websocket.StatusGoingAway, "test close")
				return
			}
		default:
			return
		}
	}
}

// muted reports whether the subscribe in payload is to the muted resource.
func (s *connTestServer) muted(payload []json.RawMessage) bool {
	resource, _ := s.mutedResource.Load().(string)
	if resource == "" {
		return true
	}
	var uri string
	return len(payload) > 2 && json.Unmarshal(payload[2], &uri) == nil && uri == resource
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

// assertAtomicUint32Stays fails if counter changes from want before a short
// deadline. It is used for negative lifecycle assertions where the code should
// not close or redial a WebSocket.
func assertAtomicUint32Stays(t *testing.T, counter *atomic.Uint32, want uint32, name string) {
	t.Helper()
	deadline := time.After(100 * time.Millisecond)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		got := counter.Load()
		if got != want {
			t.Fatalf("%s = %d, want %d", name, got, want)
		}
		select {
		case <-deadline:
			return
		case <-tick.C:
		}
	}
}

// A nil *Conn passed through a Provider must fail with ErrUnavailable, not panic.
func TestNilConnReportsUnavailable(t *testing.T) {
	var conn *Conn
	p := NewProvider(conn, conn)
	sub := NewSubscription("test-resource", nil)
	if err := p.Subscribe(context.Background(), sub); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Subscribe error = %v, want %v", err, ErrUnavailable)
	}
	if err := p.Unsubscribe(context.Background(), sub); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Unsubscribe error = %v, want %v", err, ErrUnavailable)
	}
}
