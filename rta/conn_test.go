package rta

import (
	"context"
	"encoding/json"
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

	conn.reconnectMu.Lock()
	conn.reconnectDone = first
	conn.reconnectMu.Unlock()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- conn.wait(context.Background())
	}()

	time.Sleep(50 * time.Millisecond)
	conn.reconnectMu.Lock()
	conn.reconnectDone = second
	conn.reconnectMu.Unlock()
	close(first)

	select {
	case err := <-waitDone:
		t.Fatalf("Wait returned early with %v while chained reconnect was still active", err)
	case <-time.After(50 * time.Millisecond):
	}

	conn.reconnectMu.Lock()
	conn.reconnectDone = nil
	conn.reconnectMu.Unlock()
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
	conn := &Conn{}
	conn.ctx, conn.cancel = context.WithCancelCause(context.Background())
	conn.subscriptions = make(map[uint32]*Subscription)
	conn.pending = make(map[*Subscription]struct{})
	for i := range cap(conn.expected) {
		conn.expected[i] = make(map[uint32]chan<- *handshake)
	}
	return conn
}

func TestDrainExpectedDoesNotResetSequences(t *testing.T) {
	conn := newTestConn()
	conn.expected[operationSubscribe] = map[uint32]chan<- *handshake{1: make(chan *handshake)}
	conn.expected[operationUnsubscribe] = map[uint32]chan<- *handshake{1: make(chan *handshake)}
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
	if current := conn.subscriptions[2]; current != subscription {
		t.Fatal("subscription was not registered with the replacement connection ID")
	}
	if _, ok := conn.subscriptions[1]; ok {
		t.Fatal("stale subscription ID was registered")
	}
}

func TestUnsubscribeDeletesAttemptedIDAfterReconnectRace(t *testing.T) {
	conn := newTestConn()
	subscription := &Subscription{ID: 1}
	subscription.setCurrent(1, nil)
	conn.subscriptions[1] = subscription
	conn.expectHook = func(op uint8, sequence uint32, payload []any) (<-chan *handshake, chan struct{}, error) {
		if got := payload[0].(uint32); got != 1 {
			t.Fatalf("unsubscribe ID = %d, want 1", got)
		}
		subscription.setCurrent(2, nil)
		conn.subscriptions[2] = subscription
		ch := make(chan *handshake, 1)
		ch <- &handshake{status: StatusOK}
		return ch, conn.currentReaderDone(), nil
	}

	if err := conn.Unsubscribe(context.Background(), subscription); err != nil {
		t.Fatalf("Unsubscribe returned error: %v", err)
	}
	if _, ok := conn.subscriptions[1]; ok {
		t.Fatal("old subscription ID was not deleted")
	}
	if current := conn.subscriptions[2]; current != subscription {
		t.Fatal("replacement subscription ID was deleted")
	}
}

func TestSubscriptionCurrentCustomUsesReconnectPayload(t *testing.T) {
	subscription := &Subscription{
		Custom: json.RawMessage(`{"ConnectionId":"00000000-0000-0000-0000-000000000001"}`),
	}
	subscription.setCurrent(2, json.RawMessage(`{"ConnectionId":"00000000-0000-0000-0000-000000000002"}`))

	if got := string(subscription.CurrentCustom()); got != `{"ConnectionId":"00000000-0000-0000-0000-000000000002"}` {
		t.Fatalf("CurrentCustom = %s, want reconnect payload", got)
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
				time.Sleep(20 * time.Millisecond)
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

func TestWaitBlocksUntilReconnectErrorHandlersFinish(t *testing.T) {
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
		t.Fatalf("Wait returned early with %v while reconnect error handler was still running", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseErrorHandler)

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("Wait returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Wait after reconnect error handler finished")
	}
}

type testSubscriptionHandler struct {
	handleReconnect      func()
	handleReconnectError func(error)
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
