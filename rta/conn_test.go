package rta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
)

type testServer struct {
	http    *httptest.Server
	handler func(conn *websocket.Conn)
	testing.TB
}

func (s *testServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.Errorf("accept websocket: %v", err)
		return
	}
	go s.handler(conn)
}

func serve(t testing.TB, handler func(conn *websocket.Conn)) {
	s := &testServer{TB: t, handler: handler}
	s.http = httptest.NewServer(s)
	t.Cleanup(s.http.Close)

	var err error
	connectURL, err = url.Parse(s.http.URL)
	if err != nil {
		t.Fatalf("error parsing URL: %s", err)
	}
	connectURL.Scheme, connectURL.Path = "ws", "/connect"
}

func TestReconnect(t *testing.T) {
	var id uint32
	serve(t, func(conn *websocket.Conn) {
		defer conn.Close(websocket.StatusNormalClosure, "server close")

		for {
			_, b, err := conn.Read(t.Context())
			if err != nil {
				return
			}
			sequence, req, err := parseRequest(b)
			if err != nil {
				t.Fatal(err)
			}
			switch req := req.(type) {
			case *subscribeRequest:
				id++
				if req.resourceURI != "resource://session" {
					t.Fatalf("resource URI != %q, expected %q", req.resourceURI, "resource://session")
				}
				writeSubscribeHandshake(t, conn, "subscription", sequence, id, map[string]any{"ConnectionId": uuid.New()})
			default:
				t.Fatalf("received non-subscribe request: %T", req)
			}
		}
	})

	subscriptionErr := make(chan error, 1)
	conn, err := Dial(t.Context(), http.DefaultClient, slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	if err != nil {
		t.Fatalf("dial RTA connection: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-subscriptionErr:
			if !errors.Is(err, net.ErrClosed) {
				t.Fatalf("error is not net.ErrClosed")
			}
			return
		case <-time.After(time.Second * 2):
			t.Fatalf("timed out waiting for the subscription to report connection closure")
		}
	}()

	subscription, err := conn.Subscribe(t.Context(), "resource://session")
	if err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if got := subscription.ID(); got != 1 {
		t.Fatalf("initial subscription.ID() = %d, expected 1", got)
	}
	t.Logf("subscribed, id=%d", subscription.ID())

	reconnected := make(chan struct{}, 1)
	subscription.Handle(testSubscriptionHandler{
		handleReconnect: func() {
			if got := subscription.ID(); got != 2 {
				t.Fatalf("subscription.ID() = %d, expected 2", got)
			}
			t.Logf("reconnected, id=%d", subscription.ID())
			reconnected <- struct{}{}
		},
		handleError: func(err error) {
			subscriptionErr <- err
		},
	})

	_ = conn.conn.Close(websocket.StatusNormalClosure, "")

	select {
	case <-reconnected:
		return
	case <-time.After(time.Second * 2):
		t.Fatalf("timed out waiting for the subscription to be reconnected")
	}
}

type testSubscriptionHandler struct {
	handleReconnect func()
	handleResync    func()
	handleEvent     func(json.RawMessage)
	handleError     func(error)
}

func (h testSubscriptionHandler) HandleReconnect() {
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

func (h testSubscriptionHandler) HandleError(err error) {
	if h.handleError != nil {
		h.handleError(err)
	}
}

func writeSubscribeHandshake(t *testing.T, conn *websocket.Conn, label string, sequence, id uint32, custom any) bool {
	t.Helper()

	c, err := json.Marshal(custom)
	if err != nil {
		t.Fatalf("encode custom: %s", err)
	}
	if err := wsjson.Write(context.Background(), conn, []any{
		typeSubscribe,
		sequence,
		StatusOK,
		id,
		c,
	}); err != nil {
		t.Errorf("write %s subscribe handshake: %v", label, err)
		return false
	}
	return true
}

func parseRequest(b []byte) (sequence uint32, req request, _ error) {
	var payload []json.RawMessage
	if err := json.Unmarshal(b, &payload); err != nil {
		return 0, nil, fmt.Errorf("decode payload: %w", err)
	}
	if len(payload) < 2 {
		return 0, nil, fmt.Errorf("payload length must be >= 2")
	}
	var typ uint32
	if err := json.Unmarshal(payload[0], &typ); err != nil {
		return 0, nil, fmt.Errorf("decode request type: %w", err)
	}
	if err := json.Unmarshal(payload[1], &sequence); err != nil {
		return 0, nil, fmt.Errorf("read sequence: %w", err)
	}
	switch typ {
	case typeSubscribe:
		req = &subscribeRequest{}
	default:
		return 0, nil, fmt.Errorf("invalid request type: %d", typ)
	}
	if err := req.unmarshalPayload(payload[2:]); err != nil {
		return 0, nil, fmt.Errorf("decode request: %w", err)
	}
	return sequence, req, nil
}

type request interface {
	Type() uint32
	unmarshalPayload(payload []json.RawMessage) error
}

type subscribeRequest struct {
	resourceURI string
}

func (req *subscribeRequest) Type() uint32 {
	return typeSubscribe
}

func (req *subscribeRequest) unmarshalPayload(payload []json.RawMessage) error {
	if len(payload) != 1 {
		return fmt.Errorf("%d != 1", len(payload))
	}
	if err := json.Unmarshal(payload[0], &req.resourceURI); err != nil {
		return fmt.Errorf("decode resource URI: %w", err)
	}
	return nil
}
