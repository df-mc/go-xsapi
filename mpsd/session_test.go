package mpsd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
	"github.com/google/uuid"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestJoinWithoutRTAFailsBeforeRequest(t *testing.T) {
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected request")
	})}
	client := New(httpClient, nil, xsts.UserInfo{XUID: "1"}, nil)

	_, err := client.Join(context.Background(), uuid.New(), JoinConfig{})
	if !errors.Is(err, rta.ErrUnavailable) {
		t.Fatalf("Join error = %v, want %v", err, rta.ErrUnavailable)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestSessionConstantsReturnsDetachedCopy(t *testing.T) {
	session := testSessionWithCache()

	constants := session.Constants()
	constants.System.Initiators[0] = "mutated-initiator"
	constants.Custom[0] = 'X'

	if got := session.cache.Constants.System.Initiators[0]; got != "original-initiator" {
		t.Fatalf("cache initiators mutated: got %q", got)
	}
	if got := string(session.cache.Constants.Custom); got != `{"constant":"original"}` {
		t.Fatalf("cache constants custom mutated: got %s", got)
	}
}

func TestSessionPropertiesReturnsDetachedCopy(t *testing.T) {
	session := testSessionWithCache()

	properties := session.Properties()
	properties.System.Keywords[0] = "mutated-keyword"
	properties.Custom[0] = 'X'

	if got := session.cache.Properties.System.Keywords[0]; got != "original-keyword" {
		t.Fatalf("cache keywords mutated: got %q", got)
	}
	if got := string(session.cache.Properties.Custom); got != `{"property":"original"}` {
		t.Fatalf("cache properties custom mutated: got %s", got)
	}
}

func TestSessionMemberReturnsDetachedCopy(t *testing.T) {
	session := testSessionWithCache()

	member, ok := session.Member("me")
	if !ok {
		t.Fatal("expected member snapshot")
	}
	member.Properties.System.Subscription.ChangeTypes[0] = ChangeTypeHost
	member.Properties.System.SecureDeviceAddress[0] = 9

	gotMember := session.cache.Members["me"]
	if got := gotMember.Properties.System.Subscription.ChangeTypes[0]; got != ChangeTypeEverything {
		t.Fatalf("cache change types mutated: got %q", got)
	}
	if got := gotMember.Properties.System.SecureDeviceAddress[0]; got != 1 {
		t.Fatalf("cache secure device address mutated: got %d", got)
	}
}

func TestSessionMembersReturnsDetachedCopies(t *testing.T) {
	session := testSessionWithCache()

	for _, listed := range session.Members() {
		listed.Constants.System.XUID = "mutated-from-iterator"
		listed.Properties.Custom[0] = 'Y'
	}

	gotMember := session.cache.Members["me"]
	if got := gotMember.Constants.System.XUID; got != "original-xuid" {
		t.Fatalf("cache member xuid mutated: got %q", got)
	}
	if got := string(gotMember.Properties.Custom); got != `{"memberProperty":"original"}` {
		t.Fatalf("cache member properties custom mutated: got %s", got)
	}
}

func testSessionWithCache() *Session {
	return &Session{
		cache: SessionDescription{
			Constants: &SessionConstants{
				System: &SessionConstantsSystem{
					Visibility:   "open",
					Initiators:   []string{"original-initiator"},
					Capabilities: json.RawMessage(`{"capability":true}`),
				},
				Custom: json.RawMessage(`{"constant":"original"}`),
			},
			Properties: &SessionProperties{
				System: &SessionPropertiesSystem{
					Keywords:                         []string{"original-keyword"},
					Turn:                             []uint32{1},
					Matchmaking:                      json.RawMessage(`{"mode":"original"}`),
					ServerConnectionStringCandidates: json.RawMessage(`["original"]`),
				},
				Custom: json.RawMessage(`{"property":"original"}`),
			},
			Members: map[string]*MemberDescription{
				"me": {
					Constants: &MemberConstants{
						System: &MemberConstantsSystem{
							XUID:       "original-xuid",
							Initialize: true,
						},
						Custom: json.RawMessage(`{"memberConstant":"original"}`),
					},
					Properties: &MemberProperties{
						System: &MemberPropertiesSystem{
							Active:              true,
							Connection:          uuid.New(),
							Subscription:        &MemberPropertiesSystemSubscription{ID: "SUB", ChangeTypes: []string{ChangeTypeEverything}},
							SecureDeviceAddress: []byte{1, 2, 3},
						},
						Custom: json.RawMessage(`{"memberProperty":"original"}`),
					},
				},
			},
		},
	}
}

func TestSessionUpdateReturnsDeletedOnNoContent(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	oldState := SessionDescription{
		Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
	}

	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
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

	session := &Session{
		client: &Client{client: httpClient},
		ref:    ref,
		etag:   `"old-etag"`,
		cache:  oldState,
		closed: make(chan struct{}),
	}

	deleted, err := session.update(context.Background(), SessionDescription{
		Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"patched"}`)},
	}, nil)
	if err != nil {
		t.Fatalf("update returned error: %v", err)
	}
	if !deleted {
		t.Fatal("update did not report deleted on 204")
	}

	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if got := session.etag; got != `"old-etag"` {
		t.Fatalf("etag = %q, want unchanged", got)
	}
	if got := string(session.cache.Properties.Custom); got != `{"property":"old"}` {
		t.Fatalf("cache mutated during update: got %s", got)
	}
}

func TestSessionSyncWaitsForInFlightUpdate(t *testing.T) {
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

	session := &Session{
		client: &Client{client: httpClient},
		ref:    ref,
		etag:   `"old-etag"`,
		closed: make(chan struct{}),
	}

	updateDone := make(chan error, 1)
	go func() {
		_, err := session.update(context.Background(), SessionDescription{}, nil)
		updateDone <- err
	}()
	<-updateStarted

	syncDone := make(chan error, 1)
	go func() {
		syncDone <- session.Sync(context.Background())
	}()

	select {
	case <-syncStarted:
		t.Fatal("Sync started an HTTP request while update was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseUpdate)
	if err := <-updateDone; err != nil {
		t.Fatalf("update returned error: %v", err)
	}
	if err := <-syncDone; err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	select {
	case <-syncStarted:
	case <-time.After(time.Second):
		t.Fatal("Sync did not start after update finished")
	}
}

func TestSessionSetCustomPropertiesMarksDeletedOnNoContent(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     http.StatusText(http.StatusNoContent),
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	session := &Session{
		client: &Client{client: httpClient},
		ref:    ref,
		etag:   `"old-etag"`,
		cache: SessionDescription{
			Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
		},
		closed: make(chan struct{}),
	}

	if err := session.SetCustomProperties(context.Background(), json.RawMessage(`{"property":"patched"}`)); err != nil {
		t.Fatalf("SetCustomProperties returned error: %v", err)
	}
	if session.cache.Properties != nil {
		t.Fatalf("cache properties not cleared: %+v", session.cache.Properties)
	}
	if session.cache.Constants != nil || session.cache.Members != nil || session.cache.RoleTypes != nil {
		t.Fatalf("cache not cleared after delete: %+v", session.cache)
	}
	if got := session.etag; got != "" {
		t.Fatalf("etag = %q, want empty", got)
	}
	if err := session.Context().Err(); err != context.Canceled {
		t.Fatalf("session context err = %v, want %v", err, context.Canceled)
	}
	if err := session.Sync(context.Background()); err != net.ErrClosed {
		t.Fatalf("Sync error = %v, want %v", err, net.ErrClosed)
	}
}

func TestSessionCloseContextClosesHandleWithoutClearingSyncedState(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("request method = %s, want PUT", req.Method)
		}
		header := make(http.Header)
		header.Set("ETag", `"new-etag"`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Body: io.NopCloser(bytes.NewReader([]byte(`{
				"properties": {
					"custom": {"property":"patched"}
				}
			}`))),
			Header:  header,
			Request: req,
		}, nil
	})}

	client := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	session := &Session{
		client: client,
		ref:    ref,
		etag:   `"old-etag"`,
		cache: SessionDescription{
			Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
		},
		closed: make(chan struct{}),
	}
	client.sessions[ref.URL().String()] = session

	if err := session.CloseContext(context.Background()); err != nil {
		t.Fatalf("CloseContext returned error: %v", err)
	}
	if got := string(session.cache.Properties.Custom); got != `{"property":"patched"}` {
		t.Fatalf("cache custom = %s, want synced response", got)
	}
	if got := session.etag; got != `"new-etag"` {
		t.Fatalf("etag = %q, want %q", got, `"new-etag"`)
	}
	if _, ok := client.sessions[ref.URL().String()]; ok {
		t.Fatal("session still registered after close")
	}
	if err := session.Context().Err(); err != context.Canceled {
		t.Fatalf("session context err = %v, want %v", err, context.Canceled)
	}
	if err := session.Sync(context.Background()); err != net.ErrClosed {
		t.Fatalf("Sync error = %v, want %v", err, net.ErrClosed)
	}
}

func TestSessionCloseContextConcurrentCloseSendsSingleUpdate(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("request method = %s, want PUT", req.Method)
		}
		if n := requests.Add(1); n == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		header := make(http.Header)
		header.Set("ETag", `"etag"`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
			Header:     header,
			Request:    req,
		}, nil
	})}

	session := &Session{
		client: &Client{
			client:   httpClient,
			sessions: map[string]*Session{},
		},
		ref:    ref,
		closed: make(chan struct{}),
	}
	session.client.sessions[ref.URL().String()] = session

	// Hold closeMu so both goroutines contend on the same critical section once
	// released, instead of relying on scheduler timing.
	session.closeMu.Lock()
	errCh := make(chan error, 2)
	ready := make(chan struct{}, 2)
	for range 2 {
		go func() {
			ready <- struct{}{}
			errCh <- session.CloseContext(context.Background())
		}()
	}
	<-ready
	<-ready
	session.closeMu.Unlock()
	<-firstStarted
	close(releaseFirst)

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("CloseContext returned error: %v", err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	if err := session.Context().Err(); err != context.Canceled {
		t.Fatalf("session context err = %v, want %v", err, context.Canceled)
	}
}

func TestSubscriptionHandlerClosesSessionsOnUserUnsubscribe(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	requests := make(chan struct{}, 1)
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("request method = %s, want PUT", req.Method)
		}
		requests <- struct{}{}
		header := make(http.Header)
		header.Set("ETag", `"etag"`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
			Header:     header,
			Request:    req,
		}, nil
	})}

	client := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	session := &Session{
		client: client,
		ref:    ref,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		closed: make(chan struct{}),
	}
	client.sessions[ref.URL().String()] = session

	handler := &subscriptionHandler{Client: client}
	handler.HandleError(rta.ErrUnsubscribed)

	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("subscription unsubscribe did not close session")
	}
	select {
	case <-session.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("session context was not canceled")
	}
	if _, ok := client.sessions[ref.URL().String()]; ok {
		t.Fatal("session still registered after subscription unsubscribe")
	}
}

func TestSubscriptionConnectionIDRequiresActiveSubscription(t *testing.T) {
	client := NewWithRTASubscriber(nil, nil, nil, xsts.UserInfo{}, nil)
	client.subscriptionData.Store(&subscriptionData{ConnectionID: uuid.New()})

	_, err := client.subscriptionConnectionID()
	if !errors.Is(err, rta.ErrUnavailable) {
		t.Fatalf("subscriptionConnectionID error = %v, want %v", err, rta.ErrUnavailable)
	}
}

func TestSubscribeSerializesInFlightSubscribe(t *testing.T) {
	subscriber := &blockingSubscriber{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	client := NewWithRTASubscriber(nil, subscriber, nil, xsts.UserInfo{}, nil)

	firstDone := make(chan error, 1)
	go func() {
		_, err := client.subscribe(context.Background())
		firstDone <- err
	}()
	<-subscriber.started

	secondDone := make(chan error, 1)
	go func() {
		_, err := client.subscribe(context.Background())
		secondDone <- err
	}()
	select {
	case <-subscriber.secondStarted:
		t.Fatal("second subscribe entered while first subscribe was in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(subscriber.release)
	if err := <-firstDone; err == nil {
		t.Fatal("first subscribe returned nil error")
	}
	if err := <-secondDone; err == nil {
		t.Fatal("second subscribe returned nil error")
	}
}

func TestSessionConnectionReconcileSerializesWithReconnect(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	oldConnectionID := uuid.New()
	newConnectionID := uuid.New()

	updates := make(chan uuid.UUID, 2)
	releaseFirst := make(chan struct{})
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			return nil, fmt.Errorf("request method = %s, want PUT", req.Method)
		}
		var changes SessionDescription
		if err := json.NewDecoder(req.Body).Decode(&changes); err != nil {
			return nil, err
		}
		member := changes.Members["me"]
		if member == nil || member.Properties == nil || member.Properties.System == nil {
			return nil, errors.New("connection update missing me member system properties")
		}
		connectionID := member.Properties.System.Connection
		updates <- connectionID
		if n := requests.Add(1); n == 1 {
			<-releaseFirst
		}

		body, err := json.Marshal(SessionDescription{
			Members: map[string]*MemberDescription{
				"me": {
					Properties: &MemberProperties{
						System: &MemberPropertiesSystem{
							Connection: connectionID,
							Active:     true,
						},
					},
				},
			},
		})
		if err != nil {
			return nil, err
		}
		header := make(http.Header)
		header.Set("ETag", `"etag"`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     header,
			Request:    req,
		}, nil
	})}

	client := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	handler := &subscriptionHandler{
		Client: client,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	client.subscriptionData.Store(&subscriptionData{ConnectionID: oldConnectionID})
	session := &Session{
		client: client,
		ref:    ref,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		h:      NopHandler{},
		cache: SessionDescription{
			Members: map[string]*MemberDescription{
				"me": {
					Properties: &MemberProperties{
						System: &MemberPropertiesSystem{
							Connection: uuid.New(),
							Active:     true,
						},
					},
				},
			},
		},
		closed: make(chan struct{}),
	}
	client.sessions[ref.URL().String()] = session

	reconcileDone := make(chan error, 1)
	go func() {
		reconcileDone <- client.reconcileSessionConnection(context.Background(), session)
	}()
	select {
	case got := <-updates:
		if got != oldConnectionID {
			t.Fatalf("initial reconcile connection ID = %s, want %s", got, oldConnectionID)
		}
	case <-time.After(time.Second):
		t.Fatal("initial reconcile did not start")
	}

	subscribeDone := make(chan error, 1)
	custom, err := json.Marshal(subscriptionData{ConnectionID: newConnectionID})
	if err != nil {
		t.Fatalf("marshal subscription data: %v", err)
	}
	go func() {
		subscribeDone <- handler.HandleSubscribe(custom)
	}()
	select {
	case got := <-updates:
		t.Fatalf("reconnect wrote connection ID %s while initial reconcile was in flight", got)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-reconcileDone; err != nil {
		t.Fatalf("initial reconcile returned error: %v", err)
	}
	if err := <-subscribeDone; err != nil {
		t.Fatalf("HandleSubscribe returned error: %v", err)
	}
	select {
	case got := <-updates:
		if got != newConnectionID {
			t.Fatalf("reconnect connection ID = %s, want %s", got, newConnectionID)
		}
	case <-time.After(time.Second):
		t.Fatal("reconnect did not update session connection ID")
	}
}

func TestSubscriptionHandlerCallbackRunsAfterReconcileLock(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	connectionID := uuid.New()

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case http.MethodGet:
			return &http.Response{
				StatusCode: http.StatusNotModified,
				Status:     http.StatusText(http.StatusNotModified),
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		case http.MethodPut:
			body, err := json.Marshal(SessionDescription{
				Members: map[string]*MemberDescription{
					"me": {
						Properties: &MemberProperties{
							System: &MemberPropertiesSystem{
								Connection: connectionID,
								Active:     true,
							},
						},
					},
				},
			})
			if err != nil {
				return nil, err
			}
			header := make(http.Header)
			header.Set("ETag", `"etag"`)
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     http.StatusText(http.StatusOK),
				Body:       io.NopCloser(bytes.NewReader(body)),
				Header:     header,
				Request:    req,
			}, nil
		default:
			return nil, fmt.Errorf("request method = %s, want GET or PUT", req.Method)
		}
	})}

	client := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	client.subscriptionData.Store(&subscriptionData{ConnectionID: connectionID})
	callbackErr := make(chan error, 1)
	session := &Session{
		client: client,
		ref:    ref,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		h: sessionChangeFunc(func(session *Session) {
			callbackErr <- client.reconcileSessionConnection(context.Background(), session)
		}),
		cache: SessionDescription{
			Members: map[string]*MemberDescription{
				"me": {
					Properties: &MemberProperties{
						System: &MemberPropertiesSystem{
							Connection: uuid.New(),
							Active:     true,
						},
					},
				},
			},
		},
		closed: make(chan struct{}),
	}
	client.sessions[ref.URL().String()] = session

	done := make(chan struct{})
	handler := &subscriptionHandler{
		Client: client,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	go func() {
		handler.HandleResync()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HandleResync deadlocked while invoking session handler")
	}
	if err := <-callbackErr; err != nil {
		t.Fatalf("callback reconcile returned error: %v", err)
	}
}

type sessionChangeFunc func(*Session)

func (f sessionChangeFunc) HandleSessionChange(session *Session) {
	f(session)
}

type blockingSubscriber struct {
	started       chan struct{}
	secondStarted chan struct{}
	release       chan struct{}
	calls         atomic.Int32
}

func (s *blockingSubscriber) Subscribe(context.Context, *rta.Subscription) error {
	switch s.calls.Add(1) {
	case 1:
		s.secondStarted = make(chan struct{})
		close(s.started)
		<-s.release
	case 2:
		close(s.secondStarted)
	}
	return errors.New("subscribe failed")
}
