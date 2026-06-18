package mpsd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/google/uuid"
)

func TestSubscriptionConnectionIDRejectsMissingData(t *testing.T) {
	var c Client

	_, err := c.subscriptionConnectionID()
	if err == nil {
		t.Fatal("subscriptionConnectionID returned nil error, want missing data error")
	}
	if !strings.Contains(err.Error(), "missing RTA subscription data") {
		t.Fatalf("subscriptionConnectionID error = %v, want missing data", err)
	}
}

func TestSubscriptionHandlerRejectsMissingConnectionID(t *testing.T) {
	c := &Client{sessions: make(map[string]*Session)}
	h := &subscriptionHandler{
		Client: c,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := h.HandleSubscribe(json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("HandleSubscribe returned nil error, want missing connection ID error")
	}
	if !strings.Contains(err.Error(), "missing RTA connection ID") {
		t.Fatalf("HandleSubscribe error = %v, want missing connection ID", err)
	}
	if data := c.subscriptionData.Load(); data != nil {
		t.Fatalf("subscription data was cached after invalid payload: %+v", data)
	}
}

func TestSubscriptionHandlerStoresValidConnectionID(t *testing.T) {
	c := &Client{sessions: make(map[string]*Session)}
	h := &subscriptionHandler{
		Client: c,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	id := uuid.New()

	if err := h.HandleSubscribe(json.RawMessage(`{"ConnectionId":"` + id.String() + `"}`)); err != nil {
		t.Fatalf("HandleSubscribe returned error: %v", err)
	}
	got, err := c.subscriptionConnectionID()
	if err != nil {
		t.Fatalf("subscriptionConnectionID returned error: %v", err)
	}
	if got != id {
		t.Fatalf("connection ID = %v, want %v", got, id)
	}
}

func TestSubscriptionHandlerIgnoresClosedSessionDuringSubscribe(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	closedRef := ref
	closedRef.Name = "CLOSED"
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	c := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	active := &Session{
		client: c,
		ref:    ref,
		closed: make(chan struct{}),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	closed := &Session{
		client: c,
		ref:    closedRef,
		closed: make(chan struct{}),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	close(closed.closed)
	c.sessions[active.ref.URL().String()] = active
	c.sessions[closed.ref.URL().String()] = closed
	h := &subscriptionHandler{
		Client: c,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	id := uuid.New()

	if err := h.HandleSubscribe(json.RawMessage(`{"ConnectionId":"` + id.String() + `"}`)); err != nil {
		t.Fatalf("HandleSubscribe returned error: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("session update requests = %d, want 1", got)
	}
	select {
	case <-active.Context().Done():
		t.Fatal("active session was closed")
	default:
	}
}

func TestSubscriptionHandlerReturnsSessionConnectionUpdateError(t *testing.T) {
	wantErr := errors.New("update failed")
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, wantErr
	})}
	c := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	session := &Session{
		client: c,
		ref:    ref,
		closed: make(chan struct{}),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	c.sessions[ref.URL().String()] = session
	h := &subscriptionHandler{
		Client: c,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	id := uuid.New()

	err := h.HandleSubscribe(json.RawMessage(`{"ConnectionId":"` + id.String() + `"}`))
	if !errors.Is(err, wantErr) {
		t.Fatalf("HandleSubscribe error = %v, want %v", err, wantErr)
	}
}

func TestSubscriptionHandlerResyncWaitsForConnectionUpdate(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	updateStarted := make(chan struct{})
	releaseUpdate := make(chan struct{})
	syncStarted := make(chan struct{}, 1)
	var updateOnce sync.Once
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case http.MethodPut:
			updateOnce.Do(func() { close(updateStarted) })
			<-releaseUpdate

			header := make(http.Header)
			header.Set("ETag", `"updated-etag"`)
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     http.StatusText(http.StatusOK),
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
				Header:     header,
				Request:    req,
			}, nil
		case http.MethodGet:
			syncStarted <- struct{}{}
			return &http.Response{
				StatusCode: http.StatusNotModified,
				Status:     http.StatusText(http.StatusNotModified),
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		default:
			t.Fatalf("request method = %s, want PUT or GET", req.Method)
			return nil, nil
		}
	})}

	c := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	session := &Session{
		client: c,
		ref:    ref,
		etag:   `"old-etag"`,
		closed: make(chan struct{}),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	c.sessions[ref.URL().String()] = session
	h := &subscriptionHandler{
		Client: c,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	updateDone := make(chan error, 1)
	go func() {
		_, err := session.update(context.Background(), SessionDescription{}, nil)
		updateDone <- err
	}()
	<-updateStarted

	resyncDone := make(chan struct{})
	go func() {
		h.HandleResync()
		close(resyncDone)
	}()

	select {
	case <-syncStarted:
		t.Fatal("HandleResync started Sync while connection update was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseUpdate)
	if err := <-updateDone; err != nil {
		t.Fatalf("update returned error: %v", err)
	}
	select {
	case <-resyncDone:
	case <-time.After(time.Second):
		t.Fatal("HandleResync did not finish after update finished")
	}
	select {
	case <-syncStarted:
	case <-time.After(time.Second):
		t.Fatal("HandleResync did not sync session after update finished")
	}
}

func TestSubscriptionHandlerResyncNotifiesSessionHandlerAfterSync(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("request method = %s, want GET", req.Method)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	c := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	session := &Session{
		client: c,
		ref:    ref,
		closed: make(chan struct{}),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	changes := make(chan *Session, 1)
	session.Handle(recordingSessionHandler{changes: changes})
	c.sessions[ref.URL().String()] = session
	h := &subscriptionHandler{
		Client: c,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	h.HandleResync()

	select {
	case got := <-changes:
		if got != session {
			t.Fatal("HandleSessionChange received the wrong session")
		}
	case <-time.After(time.Second):
		t.Fatal("HandleResync did not notify session handler")
	}
}

func TestCreateSessionReconcilesCurrentSubscriptionConnection(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	oldConnectionID := uuid.New()
	currentConnectionID := uuid.New()
	var activityRequests atomic.Int32
	var connectionUpdates atomic.Int32
	var c *Client
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case http.MethodPost:
			activityRequests.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     http.StatusText(http.StatusOK),
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		case http.MethodPut:
			connectionUpdates.Add(1)
			c.sessionsMu.RLock()
			_, tracked := c.sessions[ref.URL().String()]
			c.sessionsMu.RUnlock()
			if !tracked {
				t.Fatal("session was not tracked during connection reconciliation")
			}
			var update SessionDescription
			if err := json.NewDecoder(req.Body).Decode(&update); err != nil {
				t.Fatalf("decode update body: %v", err)
			}
			member := update.Members["me"]
			if member == nil || member.Properties == nil || member.Properties.System == nil {
				t.Fatalf("connection update body missing me properties: %+v", update)
			}
			if got := member.Properties.System.Connection; got != currentConnectionID {
				t.Fatalf("connection update ID = %v, want %v", got, currentConnectionID)
			}
			var body bytes.Buffer
			if err := json.NewEncoder(&body).Encode(SessionDescription{
				Members: map[string]*MemberDescription{
					"me": {
						Properties: &MemberProperties{
							System: &MemberPropertiesSystem{
								Active:     true,
								Connection: currentConnectionID,
							},
						},
					},
				},
			}); err != nil {
				t.Fatal(err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     http.StatusText(http.StatusOK),
				Body:       io.NopCloser(&body),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		default:
			t.Fatalf("request method = %s, want POST or PUT", req.Method)
			return nil, nil
		}
	})}
	c = &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	c.subscriptionData.Store(&subscriptionData{ConnectionID: currentConnectionID})
	initial := SessionDescription{
		Members: map[string]*MemberDescription{
			"me": {
				Properties: &MemberProperties{
					System: &MemberPropertiesSystem{
						Active:     true,
						Connection: oldConnectionID,
					},
				},
			},
		},
	}
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(initial); err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{
		StatusCode: http.StatusCreated,
		Status:     http.StatusText(http.StatusCreated),
		Body:       io.NopCloser(&body),
		Header:     make(http.Header),
	}

	session, err := c.createSession(context.Background(), ref, resp)
	if err != nil {
		t.Fatalf("createSession returned error: %v", err)
	}
	if got := activityRequests.Load(); got != 1 {
		t.Fatalf("activity requests = %d, want 1", got)
	}
	if got := connectionUpdates.Load(); got != 1 {
		t.Fatalf("connection updates = %d, want 1", got)
	}
	member, ok := session.Member("me")
	if !ok || member.Properties == nil || member.Properties.System == nil {
		t.Fatal("session cache missing me member after reconciliation")
	}
	if got := member.Properties.System.Connection; got != currentConnectionID {
		t.Fatalf("cached connection ID = %v, want %v", got, currentConnectionID)
	}
}

func TestCreateSessionUntracksSessionWhenReconcileAndCleanupFail(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	currentConnectionID := uuid.New()
	reconcileErr := errors.New("reconcile failed")
	closeErr := errors.New("close failed")
	var activityRequests atomic.Int32
	var sessionUpdates atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case http.MethodPost:
			activityRequests.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     http.StatusText(http.StatusOK),
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		case http.MethodPut:
			if sessionUpdates.Add(1) == 1 {
				return nil, reconcileErr
			}
			return nil, closeErr
		default:
			t.Fatalf("request method = %s, want POST or PUT", req.Method)
			return nil, nil
		}
	})}
	c := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	c.subscriptionData.Store(&subscriptionData{ConnectionID: currentConnectionID})
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(SessionDescription{
		Members: map[string]*MemberDescription{
			"me": {
				Properties: &MemberProperties{
					System: &MemberPropertiesSystem{Active: true},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{
		StatusCode: http.StatusCreated,
		Status:     http.StatusText(http.StatusCreated),
		Body:       io.NopCloser(&body),
		Header:     make(http.Header),
	}

	session, err := c.createSession(context.Background(), ref, resp)
	if session != nil {
		t.Fatalf("createSession session = %v, want nil", session)
	}
	if !errors.Is(err, reconcileErr) {
		t.Fatalf("createSession error = %v, want %v", err, reconcileErr)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("createSession error = %v, want joined close error %v", err, closeErr)
	}
	if got := activityRequests.Load(); got != 1 {
		t.Fatalf("activity requests = %d, want 1", got)
	}
	if got := sessionUpdates.Load(); got != 2 {
		t.Fatalf("session update requests = %d, want 2", got)
	}
	c.sessionsMu.RLock()
	_, tracked := c.sessions[ref.URL().String()]
	c.sessionsMu.RUnlock()
	if tracked {
		t.Fatal("session remained tracked after reconcile and cleanup failed")
	}
}

func TestSubscriptionHandlerIgnoresUserUnsubscribe(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	c := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	session := &Session{
		client: c,
		ref:    ref,
		closed: make(chan struct{}),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	c.sessions[ref.URL().String()] = session
	h := &subscriptionHandler{
		Client: c,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	h.HandleError(rta.ErrUnsubscribed)

	select {
	case <-session.Context().Done():
		t.Fatal("session was closed after intentional RTA unsubscribe")
	case <-time.After(50 * time.Millisecond):
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("close requests = %d, want 0", got)
	}
}

func TestSubscriptionHandlerClosesSessionsOnSubscriptionLoss(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if req.Method != http.MethodPut {
			t.Fatalf("request method = %s, want PUT", req.Method)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     http.StatusText(http.StatusNoContent),
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
	c := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	session := &Session{
		client: c,
		ref:    ref,
		closed: make(chan struct{}),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	c.sessions[ref.URL().String()] = session
	h := &subscriptionHandler{
		Client: c,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	h.HandleError(io.ErrUnexpectedEOF)

	select {
	case <-session.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("session was not closed after subscription loss")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("close requests = %d, want 1", got)
	}
	c.sessionsMu.RLock()
	_, tracked := c.sessions[ref.URL().String()]
	c.sessionsMu.RUnlock()
	if tracked {
		t.Fatal("session remained tracked after subscription loss close")
	}
}

type recordingSessionHandler struct {
	changes chan<- *Session
}

func (h recordingSessionHandler) HandleSessionChange(session *Session) {
	h.changes <- session
}
