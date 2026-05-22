package rta

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestWaitBlocksAcrossChainedReconnects(t *testing.T) {
	conn := newTestConn()
	first := make(chan struct{})
	second := make(chan struct{})

	conn.reconnectState.mu.Lock()
	conn.reconnectState.done = first
	conn.reconnectState.mu.Unlock()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- conn.wait(context.Background())
	}()

	time.Sleep(50 * time.Millisecond)
	conn.reconnectState.mu.Lock()
	conn.reconnectState.done = second
	conn.reconnectState.mu.Unlock()
	close(first)

	select {
	case err := <-waitDone:
		t.Fatalf("Wait returned early with %v while chained reconnect was still active", err)
	case <-time.After(50 * time.Millisecond):
	}

	conn.reconnectState.mu.Lock()
	conn.reconnectState.done = nil
	conn.reconnectState.mu.Unlock()
	close(second)

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Wait to unblock after chained reconnect")
	}
}

func newTestConn() *Conn {
	conn := &Conn{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	conn.ctx, conn.cancel = context.WithCancelCause(context.Background())
	conn.subscriptions = newSubscriptionRegistry()
	for i := range conn.expected {
		conn.expected[i] = make(map[uint32]expectedHandshake)
	}
	return conn
}

func TestSubscriptionCurrentCustomReturnsDetachedCopy(t *testing.T) {
	subscription := &Subscription{ID: 1, Custom: json.RawMessage(`{"value":"original"}`)}
	if subscription.Active() {
		t.Fatal("newly constructed subscription is active")
	}
	custom := subscription.CurrentCustom()
	custom[10] = 'X'
	if got := string(subscription.CurrentCustom()); got != `{"value":"original"}` {
		t.Fatalf("CurrentCustom after mutation = %s, want original", got)
	}

	setSubscriptionCurrent(subscription, 2, json.RawMessage(`{"value":"current"}`))
	if !subscription.Active() {
		t.Fatal("subscription is inactive after current state update")
	}
	custom = subscription.CurrentCustom()
	custom[10] = 'X'
	if got := string(subscription.CurrentCustom()); got != `{"value":"current"}` {
		t.Fatalf("CurrentCustom current after mutation = %s, want current", got)
	}
}

func TestCallReturnsReadyHandshakeWhenContextCancels(t *testing.T) {
	conn := newTestConn()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn.expectHook = func(op uint8, sequence uint32, payload []any) (<-chan *handshake, chan struct{}, error) {
		ch := make(chan *handshake, 1)
		ch <- &handshake{status: StatusOK}
		return ch, conn.currentReaderDone(), nil
	}

	h, _, err := conn.callWithHook(ctx, operationSubscribe, func() []any { return []any{"resource"} }, true, nil)
	if err != nil {
		t.Fatalf("callWithHook returned error: %v", err)
	}
	if h == nil || h.status != StatusOK {
		t.Fatalf("handshake = %#v, want StatusOK", h)
	}
}

func TestDrainExpectedDoesNotResetSequences(t *testing.T) {
	conn := newTestConn()
	conn.expected[operationSubscribe] = map[uint32]expectedHandshake{1: {response: make(chan *handshake)}}
	conn.expected[operationUnsubscribe] = map[uint32]expectedHandshake{1: {response: make(chan *handshake)}}
	conn.sequences[operationSubscribe].Store(41)
	conn.sequences[operationUnsubscribe].Store(42)

	conn.drainExpected()

	if got := conn.sequences[operationSubscribe].Load(); got != 41 {
		t.Fatalf("subscribe sequence = %d, want 41", got)
	}
	if got := conn.sequences[operationUnsubscribe].Load(); got != 42 {
		t.Fatalf("unsubscribe sequence = %d, want 42", got)
	}
}

func TestResubscribeRetriesTransientStatuses(t *testing.T) {
	conn := newTestConn()
	subscription := &Subscription{ID: 1, resourceURI: "resource://session"}
	setSubscriptionCurrent(subscription, 1, nil)
	conn.subscriptions.byID[1] = subscription

	var attempts atomic.Int32
	conn.expectHook = func(op uint8, sequence uint32, payload []any) (<-chan *handshake, chan struct{}, error) {
		ch := make(chan *handshake, 1)
		if attempts.Add(1) == 1 {
			ch <- &handshake{status: StatusThrottled}
			return ch, conn.currentReaderDone(), nil
		}
		ch <- &handshake{
			status:  StatusOK,
			payload: []json.RawMessage{json.RawMessage(`2`), json.RawMessage(`{"ConnectionId":"00000000-0000-0000-0000-000000000002"}`)},
		}
		return ch, conn.currentReaderDone(), nil
	}

	successes := conn.resubscribe()
	if got := attempts.Load(); got != 2 {
		t.Fatalf("resubscribe attempts = %d, want 2", got)
	}
	if len(successes) != 1 || successes[0] != subscription {
		t.Fatalf("successes = %#v, want original subscription", successes)
	}
	if got := subscription.id(); got != 2 {
		t.Fatalf("subscription ID = %d, want 2", got)
	}
	if _, ok := conn.subscriptions.byID[1]; ok {
		t.Fatal("old subscription ID was not removed")
	}
	if conn.subscriptions.byID[2] != subscription {
		t.Fatal("subscription was not registered with retried ID")
	}
}

func TestSubscribeRetriesIfConnectionChangesBeforeRegistration(t *testing.T) {
	conn := newTestConn()
	oldReaderDone := make(chan struct{})
	newReaderDone := make(chan struct{})
	conn.readerDone = oldReaderDone

	var calls atomic.Int32
	conn.dialer = &dialer{}
	conn.expectHook = func(op uint8, sequence uint32, payload []any) (<-chan *handshake, chan struct{}, error) {
		called := calls.Add(1)
		ch := make(chan *handshake, 1)
		if called == 1 {
			close(oldReaderDone)
			conn.readerDone = newReaderDone
			ch <- &handshake{
				status:  StatusOK,
				payload: []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`{"ConnectionId":"00000000-0000-0000-0000-000000000001"}`)},
			}
			return ch, oldReaderDone, nil
		}
		ch <- &handshake{
			status:  StatusOK,
			payload: []json.RawMessage{json.RawMessage(`2`), json.RawMessage(`{"ConnectionId":"00000000-0000-0000-0000-000000000002"}`)},
		}
		return ch, newReaderDone, nil
	}

	subscription, err := conn.Subscribe(context.Background(), "resource")
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("subscribe attempts = %d, want 2", got)
	}
	if got := subscription.id(); got != 2 {
		t.Fatalf("subscription ID = %d, want 2", got)
	}
	if current := conn.subscriptions.byID[2]; current != subscription {
		t.Fatal("subscription was not registered with the replacement connection ID")
	}
	if _, ok := conn.subscriptions.byID[1]; ok {
		t.Fatal("stale subscription ID was registered")
	}
}

func TestSubscribeDispatchesEventArrivingBeforeHandler(t *testing.T) {
	conn := newTestConn()
	readerDone := make(chan struct{})
	conn.readerDone = readerDone
	eventReceived := make(chan json.RawMessage, 1)

	conn.expectHook = func(op uint8, sequence uint32, payload []any) (<-chan *handshake, chan struct{}, error) {
		ch := make(chan *handshake, 1)
		ch <- &handshake{
			status: StatusOK,
			payload: []json.RawMessage{
				json.RawMessage(`7`),
				json.RawMessage(`{"ConnectionId":"00000000-0000-0000-0000-000000000001"}`),
			},
		}
		return ch, readerDone, nil
	}

	subscription, err := conn.Subscribe(context.Background(), "resource")
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	conn.handleMessage(typeEvent, []json.RawMessage{json.RawMessage(`7`), json.RawMessage(`{"event":"early"}`)})
	subscription.Handle(testSubscriptionHandler{
		handleEvent: func(custom json.RawMessage) {
			eventReceived <- custom
		},
	})

	select {
	case payload := <-eventReceived:
		if got := string(payload); got != `{"event":"early"}` {
			t.Fatalf("event payload = %s, want early event", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for early event")
	}
}

func TestSubscribeReceivesEventSentImmediatelyAfterAck(t *testing.T) {
	originalURL := connectURL
	defer func() { connectURL = originalURL }()

	eventReceived := make(chan json.RawMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		go func() {
			defer conn.Close(websocket.StatusNormalClosure, "")
			sequence, _, ok := readSubscribeRequest(t, conn, 1)
			if !ok {
				return
			}
			if !writeSubscribeHandshake(t, conn, "initial", sequence, 7, `{"ConnectionId":"00000000-0000-0000-0000-000000000001"}`) {
				return
			}
			if !writeEvent(t, conn, "immediate", 7, `{"event":"immediate"}`) {
				return
			}
			drainServerReads(conn)
		}()
	}))
	defer server.Close()
	useTestConnectURL(t, server.URL)

	conn, err := Dialer{}.DialContext(t.Context(), http.DefaultClient)
	if err != nil {
		t.Fatalf("dial RTA connection: %v", err)
	}
	defer conn.Close()

	subscription, err := conn.Subscribe(t.Context(), "resource://session")
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	subscription.Handle(testSubscriptionHandler{
		handleEvent: func(custom json.RawMessage) {
			eventReceived <- custom
		},
	})

	select {
	case payload := <-eventReceived:
		if got := string(payload); got != `{"event":"immediate"}` {
			t.Fatalf("event payload = %s, want immediate event", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for immediate event")
	}
}

func TestUnsubscribeRemovesAllIDsForSubscription(t *testing.T) {
	conn := newTestConn()
	subscription := &Subscription{ID: 1}
	setSubscriptionCurrent(subscription, 1, nil)
	conn.subscriptions.byID[1] = subscription
	conn.expectHook = func(op uint8, sequence uint32, payload []any) (<-chan *handshake, chan struct{}, error) {
		if got := payload[0].(uint32); got != 1 {
			t.Fatalf("unsubscribe ID = %d, want 1", got)
		}
		setSubscriptionCurrent(subscription, 2, nil)
		conn.subscriptions.byID[2] = subscription
		ch := make(chan *handshake, 1)
		ch <- &handshake{status: StatusOK}
		return ch, conn.currentReaderDone(), nil
	}

	if err := conn.Unsubscribe(context.Background(), subscription); err != nil {
		t.Fatalf("Unsubscribe returned error: %v", err)
	}
	if _, ok := conn.subscriptions.byID[1]; ok {
		t.Fatal("old subscription ID was not deleted")
	}
	if _, ok := conn.subscriptions.byID[2]; ok {
		t.Fatal("replacement subscription ID was not deleted")
	}
}

func TestSubscribeRegistersBeforeDeliveringHandshake(t *testing.T) {
	conn := newTestConn()
	called := make(chan json.RawMessage, 1)
	subscription := &Subscription{resourceURI: "resource"}
	response := make(chan *handshake, 1)
	conn.expected[operationSubscribe][1] = expectedHandshake{
		response: response,
		beforeDeliver: func(h *handshake) {
			if err := conn.applySubscribeHandshake(subscription, h); err != nil {
				t.Errorf("apply subscribe handshake: %v", err)
				return
			}
			conn.updateSubscriptionID(subscription, subscription.id())
		},
	}

	conn.handleMessage(typeSubscribe, []json.RawMessage{
		json.RawMessage(`1`),
		json.RawMessage(`0`),
		json.RawMessage(`7`),
		json.RawMessage(`{"ConnectionId":"00000000-0000-0000-0000-000000000001"}`),
	})
	conn.handleMessage(typeEvent, []json.RawMessage{json.RawMessage(`7`), json.RawMessage(`{"event":"after-ack"}`)})
	subscription.Handle(testSubscriptionHandler{
		handleEvent: func(custom json.RawMessage) {
			called <- custom
		},
	})

	select {
	case payload := <-called:
		if got := string(payload); got != `{"event":"after-ack"}` {
			t.Fatalf("event payload = %s, want after-ack event", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event after subscribe ack")
	}
}

func TestHandleMessageDispatchesSubscriptionEventsInOrder(t *testing.T) {
	conn := newTestConn()
	got := make(chan string, 2)
	subscription := &Subscription{ID: 7}
	subscription.Handle(testSubscriptionHandler{
		handleEvent: func(custom json.RawMessage) {
			got <- string(custom)
		},
	})
	conn.subscriptions.byID[7] = subscription

	conn.handleMessage(typeEvent, []json.RawMessage{json.RawMessage(`7`), json.RawMessage(`{"n":1}`)})
	conn.handleMessage(typeEvent, []json.RawMessage{json.RawMessage(`7`), json.RawMessage(`{"n":2}`)})

	for _, want := range []string{`{"n":1}`, `{"n":2}`} {
		select {
		case event := <-got:
			if event != want {
				t.Fatalf("event = %s, want %s", event, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %s", want)
		}
	}
}

func TestHandleMessageResyncNotifiesHandlers(t *testing.T) {
	conn := newTestConn()
	called := make(chan struct{}, 1)
	subscription := &Subscription{ID: 1}
	subscription.Handle(testSubscriptionHandler{
		handleResync: func() {
			called <- struct{}{}
		},
	})
	conn.subscriptions.byID[1] = subscription

	conn.handleMessage(typeResync, nil)

	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resync handler")
	}
}

func TestHandleMessageResyncIgnoredDuringSuppressionWindow(t *testing.T) {
	conn := newTestConn()
	called := make(chan struct{}, 1)
	subscription := &Subscription{ID: 1}
	subscription.Handle(testSubscriptionHandler{
		handleResync: func() {
			called <- struct{}{}
		},
	})
	conn.subscriptions.byID[1] = subscription
	conn.suppressResyncFor(time.Minute)

	conn.handleMessage(typeResync, nil)

	select {
	case <-called:
		t.Fatal("resync handler was called during suppression window")
	case <-time.After(50 * time.Millisecond):
	}

	conn.resyncMu.Lock()
	conn.resyncReadyAt = time.Now().Add(-time.Second)
	conn.resyncMu.Unlock()
	conn.handleMessage(typeResync, nil)

	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resync handler after suppression window")
	}
}

func TestConnReconnectsAndResubscribesAfterReadFailure(t *testing.T) {
	originalURL := connectURL
	defer func() { connectURL = originalURL }()

	var connections atomic.Int32
	closeFirstConnection := make(chan struct{})
	keepStableConnection := make(chan struct{})
	defer close(keepStableConnection)
	reconnected := make(chan struct{}, 1)
	eventReceived := make(chan json.RawMessage, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}

		number := connections.Add(1)
		go func() {
			defer conn.Close(websocket.StatusNormalClosure, "")

			sequence, resource, ok := readSubscribeRequest(t, conn, number)
			if !ok {
				return
			}
			if resource != "resource://session" {
				t.Errorf("resource URI = %q, want %q", resource, "resource://session")
				return
			}

			switch number {
			case 1:
				if !writeSubscribeHandshake(t, conn, "first", sequence, 7, `{"ConnectionId":"00000000-0000-0000-0000-000000000001"}`) {
					return
				}
				<-closeFirstConnection
				_ = conn.Close(websocket.StatusInternalError, "force reconnect")
			case 2:
				if !writeSubscribeHandshake(t, conn, "second", sequence, 8, `{"ConnectionId":"00000000-0000-0000-0000-000000000002"}`) {
					return
				}
				if !writeEvent(t, conn, "event after reconnect", 8, `{"event":"reconnected"}`) {
					return
				}
				go drainServerReads(conn)
				<-keepStableConnection
			default:
				t.Errorf("unexpected connection attempt %d", number)
			}
		}()
	}))
	defer server.Close()

	useTestConnectURL(t, server.URL)

	conn, err := Dialer{}.DialContext(t.Context(), http.DefaultClient)
	if err != nil {
		t.Fatalf("dial RTA connection: %v", err)
	}
	defer conn.Close()

	sub, err := conn.Subscribe(t.Context(), "resource://session")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	sub.Handle(testSubscriptionHandler{
		handleReconnect: func() {
			select {
			case reconnected <- struct{}{}:
			default:
			}
		},
		handleEvent: func(custom json.RawMessage) {
			select {
			case eventReceived <- custom:
			default:
			}
		},
	})
	close(closeFirstConnection)

	select {
	case <-reconnected:
	case <-time.After(time.Second * 5):
		t.Fatal("timed out waiting for reconnect callback")
	}

	select {
	case payload := <-eventReceived:
		if got := string(payload); got != `{"event":"reconnected"}` {
			t.Fatalf("event payload = %s, want reconnected event", got)
		}
	case <-time.After(time.Second * 5):
		t.Fatal("timed out waiting for event after reconnect")
	}
}

func TestConnReconnectRetriesIfReplacementSocketDropsDuringResubscribe(t *testing.T) {
	originalURL := connectURL
	defer func() { connectURL = originalURL }()

	var connections atomic.Int32
	var reconnectCalls atomic.Int32
	closeFirstConnection := make(chan struct{})
	keepStableConnection := make(chan struct{})
	defer close(keepStableConnection)
	reconnected := make(chan struct{}, 1)
	eventReceived := make(chan json.RawMessage, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}

		number := connections.Add(1)
		go func() {
			defer conn.Close(websocket.StatusNormalClosure, "")

			sequence, _, ok := readSubscribeRequest(t, conn, number)
			if !ok {
				return
			}

			switch number {
			case 1:
				if !writeSubscribeHandshake(t, conn, "first", sequence, 7, `{"ConnectionId":"00000000-0000-0000-0000-000000000001"}`) {
					return
				}
				<-closeFirstConnection
				_ = conn.Close(websocket.StatusInternalError, "force reconnect")
			case 2:
				if !writeSubscribeHandshake(t, conn, "second", sequence, 8, `{"ConnectionId":"00000000-0000-0000-0000-000000000002"}`) {
					return
				}
				_ = conn.Close(websocket.StatusInternalError, "drop replacement socket")
			case 3:
				if !writeSubscribeHandshake(t, conn, "third", sequence, 9, `{"ConnectionId":"00000000-0000-0000-0000-000000000003"}`) {
					return
				}
				time.Sleep(20 * time.Millisecond)
				if !writeEvent(t, conn, "event after second reconnect", 9, `{"event":"reconnected-twice"}`) {
					return
				}
				go drainServerReads(conn)
				<-keepStableConnection
			default:
				t.Errorf("unexpected connection attempt %d", number)
			}
		}()
	}))
	defer server.Close()

	useTestConnectURL(t, server.URL)

	conn, err := Dialer{}.DialContext(t.Context(), http.DefaultClient)
	if err != nil {
		t.Fatalf("dial RTA connection: %v", err)
	}
	defer conn.Close()

	sub, err := conn.Subscribe(t.Context(), "resource://session")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	sub.Handle(testSubscriptionHandler{
		handleReconnect: func() {
			reconnectCalls.Add(1)
			select {
			case reconnected <- struct{}{}:
			default:
			}
		},
		handleEvent: func(custom json.RawMessage) {
			select {
			case eventReceived <- custom:
			default:
			}
		},
	})
	close(closeFirstConnection)

	select {
	case <-reconnected:
	case <-time.After(time.Second * 5):
		t.Fatal("timed out waiting for reconnect callback")
	}

	select {
	case payload := <-eventReceived:
		if got := string(payload); got != `{"event":"reconnected-twice"}` {
			t.Fatalf("event payload = %s, want second reconnect event", got)
		}
	case <-time.After(time.Second * 5):
		t.Fatal("timed out waiting for event after second reconnect")
	}
	if reconnectCalls.Load() == 0 {
		t.Fatal("reconnect callback was not delivered on the surviving reconnect wave")
	}
}

func TestConnReconnectRetriesIfReplacementSocketDropsAfterSuccessfulResubscribe(t *testing.T) {
	originalURL := connectURL
	defer func() { connectURL = originalURL }()

	var connections atomic.Int32
	closeFirstConnection := make(chan struct{})
	keepStableConnection := make(chan struct{})
	defer close(keepStableConnection)
	eventReceived := make(chan json.RawMessage, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}

		number := connections.Add(1)
		go func() {
			defer conn.Close(websocket.StatusNormalClosure, "")

			sequence, _, ok := readSubscribeRequest(t, conn, number)
			if !ok {
				return
			}

			switch number {
			case 1:
				if !writeSubscribeHandshake(t, conn, "first", sequence, 7, `{"ConnectionId":"00000000-0000-0000-0000-000000000001"}`) {
					return
				}
				<-closeFirstConnection
				_ = conn.Close(websocket.StatusInternalError, "force reconnect")
			case 2:
				if !writeSubscribeHandshake(t, conn, "second", sequence, 8, `{"ConnectionId":"00000000-0000-0000-0000-000000000002"}`) {
					return
				}
				time.Sleep(120 * time.Millisecond)
				_ = conn.Close(websocket.StatusInternalError, "late replacement drop")
			case 3:
				if !writeSubscribeHandshake(t, conn, "third", sequence, 9, `{"ConnectionId":"00000000-0000-0000-0000-000000000003"}`) {
					return
				}
				time.Sleep(20 * time.Millisecond)
				if !writeEvent(t, conn, "event after late-drop reconnect", 9, `{"event":"reconnected-late-drop"}`) {
					return
				}
				go drainServerReads(conn)
				<-keepStableConnection
			default:
				t.Errorf("unexpected connection attempt %d", number)
			}
		}()
	}))
	defer server.Close()

	useTestConnectURL(t, server.URL)

	conn, err := Dialer{}.DialContext(t.Context(), http.DefaultClient)
	if err != nil {
		t.Fatalf("dial RTA connection: %v", err)
	}
	defer conn.Close()

	sub, err := conn.Subscribe(t.Context(), "resource://session")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	sub.Handle(testSubscriptionHandler{
		handleReconnect: func() {},
		handleEvent: func(custom json.RawMessage) {
			select {
			case eventReceived <- custom:
			default:
			}
		},
	})
	close(closeFirstConnection)

	select {
	case payload := <-eventReceived:
		if got := string(payload); got != `{"event":"reconnected-late-drop"}` {
			t.Fatalf("event payload = %s, want late-drop reconnect event", got)
		}
	case <-time.After(time.Second * 5):
		t.Fatal("timed out waiting for event after late-drop reconnect")
	}
}

func TestWaitDoesNotBlockOnReconnectErrorHandlers(t *testing.T) {
	originalURL := connectURL
	defer func() { connectURL = originalURL }()

	var connections atomic.Int32
	closeFirstConnection := make(chan struct{})
	errorHandlerStarted := make(chan struct{}, 1)
	releaseErrorHandler := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}

		number := connections.Add(1)
		go func() {
			defer conn.Close(websocket.StatusNormalClosure, "")

			sequence, _, ok := readSubscribeRequest(t, conn, number)
			if !ok {
				return
			}

			switch number {
			case 1:
				if !writeSubscribeHandshake(t, conn, "first", sequence, 7, `{"ConnectionId":"00000000-0000-0000-0000-000000000001"}`) {
					return
				}
				<-closeFirstConnection
				_ = conn.Close(websocket.StatusInternalError, "force reconnect")
			case 2:
				if !writeSubscribeErrorHandshake(t, conn, "second", sequence, StatusUnknownResource, `"missing resource"`) {
					return
				}
				go drainServerReads(conn)
				<-releaseErrorHandler
			default:
				t.Errorf("unexpected connection attempt %d", number)
			}
		}()
	}))
	defer server.Close()

	useTestConnectURL(t, server.URL)

	conn, err := Dialer{}.DialContext(t.Context(), http.DefaultClient)
	if err != nil {
		t.Fatalf("dial RTA connection: %v", err)
	}
	defer conn.Close()

	sub, err := conn.Subscribe(t.Context(), "resource://session")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	sub.Handle(testSubscriptionHandler{
		handleReconnectError: func(error) {
			select {
			case errorHandlerStarted <- struct{}{}:
			default:
			}
			<-releaseErrorHandler
		},
	})

	close(closeFirstConnection)

	select {
	case <-errorHandlerStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect error handler to start")
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- conn.wait(context.Background())
	}()

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait blocked on reconnect error handler")
	}
	close(releaseErrorHandler)
}

type testSubscriptionHandler struct {
	handleReconnect      func()
	handleReconnectError func(error)
	handleResync         func()
	handleEvent          func(json.RawMessage)
}

func (h testSubscriptionHandler) HandleReconnect(err error) {
	if err != nil {
		if h.handleReconnectError != nil {
			h.handleReconnectError(err)
		}
		return
	}
	if h.handleReconnect != nil {
		h.handleReconnect()
	}
}

func (h testSubscriptionHandler) HandleEvent(custom json.RawMessage) {
	if h.handleEvent != nil {
		h.handleEvent(custom)
	}
}

func (h testSubscriptionHandler) HandleResync() {
	if h.handleResync != nil {
		h.handleResync()
	}
}

func setSubscriptionCurrent(subscription *Subscription, id uint32, custom json.RawMessage) {
	subscription.mu.Lock()
	subscription.currentID = id
	subscription.currentCustom = append(json.RawMessage(nil), custom...)
	subscription.currentSet = true
	subscription.active = true
	subscription.mu.Unlock()
}

func useTestConnectURL(t *testing.T, rawURL string) {
	t.Helper()

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	u.Scheme = "ws"
	connectURL = u
}

func readSubscribeRequest(t *testing.T, conn *websocket.Conn, number int32) (sequence uint32, resource string, ok bool) {
	t.Helper()

	var request []json.RawMessage
	if err := wsjson.Read(context.Background(), conn, &request); err != nil {
		t.Logf("read subscribe request on connection %d: %v", number, err)
		return 0, "", false
	}
	if len(request) < 3 {
		t.Errorf("subscribe request on connection %d has length %d, want at least 3", number, len(request))
		return 0, "", false
	}

	var typ uint32
	if err := json.Unmarshal(request[0], &typ); err != nil {
		t.Errorf("decode message type: %v", err)
		return 0, "", false
	}
	if err := json.Unmarshal(request[1], &sequence); err != nil {
		t.Errorf("decode sequence: %v", err)
		return 0, "", false
	}
	if err := json.Unmarshal(request[2], &resource); err != nil {
		t.Errorf("decode resource URI: %v", err)
		return 0, "", false
	}
	if typ != typeSubscribe {
		t.Errorf("message type = %d, want %d", typ, typeSubscribe)
		return 0, "", false
	}
	return sequence, resource, true
}

func writeSubscribeHandshake(t *testing.T, conn *websocket.Conn, label string, sequence, id uint32, custom string) bool {
	t.Helper()

	if err := wsjson.Write(context.Background(), conn, []any{
		typeSubscribe,
		sequence,
		StatusOK,
		id,
		json.RawMessage(custom),
	}); err != nil {
		t.Errorf("write %s subscribe handshake: %v", label, err)
		return false
	}
	return true
}

func writeSubscribeErrorHandshake(t *testing.T, conn *websocket.Conn, label string, sequence uint32, status int32, message string) bool {
	t.Helper()

	if err := wsjson.Write(context.Background(), conn, []any{
		typeSubscribe,
		sequence,
		status,
		json.RawMessage(message),
	}); err != nil {
		t.Errorf("write %s subscribe error handshake: %v", label, err)
		return false
	}
	return true
}

func writeEvent(t *testing.T, conn *websocket.Conn, label string, subscriptionID uint32, custom string) bool {
	t.Helper()

	if err := wsjson.Write(context.Background(), conn, []any{
		typeEvent,
		subscriptionID,
		json.RawMessage(custom),
	}); err != nil {
		t.Errorf("write %s: %v", label, err)
		return false
	}
	return true
}

func drainServerReads(conn *websocket.Conn) {
	for {
		var payload []json.RawMessage
		if err := wsjson.Read(context.Background(), conn, &payload); err != nil {
			return
		}
	}
}
