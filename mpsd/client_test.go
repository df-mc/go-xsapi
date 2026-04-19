package mpsd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/internal/testutil"
	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
	"github.com/google/uuid"
)

type fakeUnsubscriber struct {
	attempts int
	failures int
	called   chan struct{}
	release  chan struct{}
}

func (f *fakeUnsubscriber) Unsubscribe(context.Context, *rta.Subscription) error {
	f.attempts++
	if f.called != nil {
		select {
		case f.called <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		<-f.release
	}
	if f.attempts <= f.failures {
		return errors.New("unsubscribe failed")
	}
	return nil
}

type fakeSubscriber struct {
	attempts     int
	subscription *rta.Subscription
	err          error
	failures     int
	called       chan struct{}
}

func (f *fakeSubscriber) Subscribe(context.Context, string) (*rta.Subscription, error) {
	f.attempts++
	if f.called != nil {
		select {
		case f.called <- struct{}{}:
		default:
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.attempts <= f.failures {
		return nil, errors.New("subscribe failed")
	}
	if f.subscription == nil {
		f.subscription = &rta.Subscription{}
	}
	return f.subscription, nil
}

type blockingSubscriber struct {
	started      chan struct{}
	release      chan struct{}
	subscription *rta.Subscription
}

func (b *blockingSubscriber) Subscribe(context.Context, string) (*rta.Subscription, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-b.release
	if b.subscription == nil {
		b.subscription = &rta.Subscription{}
	}
	return b.subscription, nil
}

func TestClientCloseContextPreservesSubscriptionOnUnsubscribeError(t *testing.T) {
	subscription := &rta.Subscription{}
	subscriptionData := &subscriptionData{ConnectionID: uuid.New()}
	unsub := &fakeUnsubscriber{failures: 1}
	client := &Client{
		subscription:     subscription,
		subscriptionData: subscriptionData,
		unsub:            unsub,
	}

	if err := client.CloseContext(context.Background()); err == nil {
		t.Fatal("expected unsubscribe error")
	}
	if client.subscription != subscription {
		t.Fatal("subscription was cleared after unsubscribe failure")
	}
	if client.subscriptionData != subscriptionData {
		t.Fatal("subscription data was cleared after unsubscribe failure")
	}

	if err := client.CloseContext(context.Background()); err != nil {
		t.Fatalf("retry close returned error: %v", err)
	}
	if client.subscription != nil {
		t.Fatal("subscription was not cleared after successful retry")
	}
	if client.subscriptionData != nil {
		t.Fatal("subscription data was not cleared after successful retry")
	}
	if unsub.attempts != 2 {
		t.Fatalf("unsubscribe attempts = %d, want 2", unsub.attempts)
	}
}

func TestClientCloseContextClearsStaleSubscribeBarrier(t *testing.T) {
	done := make(chan struct{})
	client := &Client{
		subscribeDone: done,
	}

	if err := client.CloseContext(context.Background()); err != nil {
		t.Fatalf("CloseContext returned error: %v", err)
	}
	if client.subscribeDone != nil {
		t.Fatal("subscribeDone was not cleared on close")
	}
}

func TestClientCloseContextSerializesConcurrentCalls(t *testing.T) {
	unsub := &fakeUnsubscriber{
		called:  make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	client := &Client{
		subscription:     &rta.Subscription{},
		subscriptionData: &subscriptionData{ConnectionID: uuid.New()},
		unsub:            unsub,
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- client.CloseContext(context.Background())
	}()

	select {
	case <-unsub.called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first close to start unsubscribe")
	}

	go func() {
		errCh <- client.CloseContext(context.Background())
	}()

	select {
	case <-unsub.called:
		t.Fatal("second close entered unsubscribe before the first completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(unsub.release)

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("CloseContext returned error: %v", err)
		}
	}
	if unsub.attempts != 1 {
		t.Fatalf("unsubscribe attempts = %d, want 1", unsub.attempts)
	}
}

func TestClientSubscribeReturnsUnavailableWithoutSubscriber(t *testing.T) {
	client := &Client{}

	_, _, err := client.subscribe(context.Background())
	if !errors.Is(err, errSubscriptionUnavailable) {
		t.Fatalf("subscribe error = %v, want %v", err, errSubscriptionUnavailable)
	}
}

func TestClientSubscribeReusesActiveCachedSubscription(t *testing.T) {
	connectionID := uuid.New()
	subscription := &rta.Subscription{}
	sub := &fakeSubscriber{}
	client := &Client{
		sub: sub,
		active: func(got *rta.Subscription) bool {
			return got == subscription
		},
		decode: func(got *rta.Subscription) (*subscriptionData, error) {
			if got != subscription {
				t.Fatalf("decoded subscription = %p, want %p", got, subscription)
			}
			return &subscriptionData{ConnectionID: connectionID}, nil
		},
	}
	client.subscription = subscription

	gotSubscription, gotData, err := client.subscribe(context.Background())
	if err != nil {
		t.Fatalf("subscribe returned error: %v", err)
	}
	if gotSubscription != subscription {
		t.Fatalf("subscription = %p, want %p", gotSubscription, subscription)
	}
	if gotData == nil || gotData.ConnectionID != connectionID {
		t.Fatalf("subscription data = %+v, want connection ID %s", gotData, connectionID)
	}
	if client.subscriptionData == nil || client.subscriptionData.ConnectionID != connectionID {
		t.Fatalf("cached subscription data = %+v, want connection ID %s", client.subscriptionData, connectionID)
	}
	if sub.attempts != 0 {
		t.Fatalf("subscribe attempts = %d, want 0", sub.attempts)
	}
}

func TestClientSubscribeRefreshesInactiveCachedSubscription(t *testing.T) {
	oldSubscription := &rta.Subscription{}
	newSubscription := &rta.Subscription{}
	connectionID := uuid.New()
	sub := &fakeSubscriber{subscription: newSubscription}
	client := &Client{
		sub: sub,
		log: slogDiscard(),
		active: func(got *rta.Subscription) bool {
			return got != oldSubscription
		},
		decode: func(got *rta.Subscription) (*subscriptionData, error) {
			if got != newSubscription {
				t.Fatalf("decoded subscription = %p, want %p", got, newSubscription)
			}
			return &subscriptionData{ConnectionID: connectionID}, nil
		},
	}
	client.subscription = oldSubscription

	gotSubscription, gotData, err := client.subscribe(context.Background())
	if err != nil {
		t.Fatalf("subscribe returned error: %v", err)
	}
	if gotSubscription != newSubscription {
		t.Fatalf("subscription = %p, want %p", gotSubscription, newSubscription)
	}
	if gotData == nil || gotData.ConnectionID != connectionID {
		t.Fatalf("subscription data = %+v, want connection ID %s", gotData, connectionID)
	}
	if client.subscription != newSubscription {
		t.Fatalf("cached subscription = %p, want %p", client.subscription, newSubscription)
	}
	if client.subscriptionData == nil || client.subscriptionData.ConnectionID != connectionID {
		t.Fatalf("cached subscription data = %+v, want connection ID %s", client.subscriptionData, connectionID)
	}
	if sub.attempts != 1 {
		t.Fatalf("subscribe attempts = %d, want 1", sub.attempts)
	}
}

func TestClientSubscribeReturnsBeforeDiscardCleanupCompletes(t *testing.T) {
	fetchedSubscription := &rta.Subscription{}
	winnerSubscription := &rta.Subscription{}
	winnerConnectionID := uuid.New()

	sub := &blockingSubscriber{
		started:      make(chan struct{}, 1),
		release:      make(chan struct{}),
		subscription: fetchedSubscription,
	}
	unsub := &fakeUnsubscriber{
		called:  make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	client := &Client{
		sub:   sub,
		unsub: unsub,
		log:   slogDiscard(),
		decode: func(subscription *rta.Subscription) (*subscriptionData, error) {
			switch subscription {
			case fetchedSubscription:
				return &subscriptionData{ConnectionID: uuid.New()}, nil
			case winnerSubscription:
				return &subscriptionData{ConnectionID: winnerConnectionID}, nil
			default:
				return nil, errors.New("unexpected subscription")
			}
		},
	}

	result := make(chan struct {
		subscription *rta.Subscription
		data         *subscriptionData
		err          error
	}, 1)
	go func() {
		subscription, data, err := client.subscribe(context.Background())
		result <- struct {
			subscription *rta.Subscription
			data         *subscriptionData
			err          error
		}{subscription: subscription, data: data, err: err}
	}()

	select {
	case <-sub.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscribe fetch")
	}

	client.subscriptionMu.Lock()
	client.subscription = winnerSubscription
	client.subscriptionMu.Unlock()

	close(sub.release)

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("subscribe returned error: %v", got.err)
		}
		if got.subscription != winnerSubscription {
			t.Fatalf("subscription = %p, want %p", got.subscription, winnerSubscription)
		}
		if got.data == nil || got.data.ConnectionID != winnerConnectionID {
			t.Fatalf("subscription data = %+v, want connection ID %s", got.data, winnerConnectionID)
		}
	case <-time.After(time.Second):
		t.Fatal("subscribe blocked behind cleanup unsubscribe")
	}

	select {
	case <-unsub.called:
	case <-time.After(time.Second):
		t.Fatal("discarded subscription was not cleaned up")
	}

	close(unsub.release)
}

func TestClientRetryReconcileSessionConnectionRepairsSession(t *testing.T) {
	connectionID1 := uuid.New()
	connectionID2 := uuid.New()
	transientErr := errors.New("transient failure")

	var (
		attempts   int
		repaired   = make(chan struct{}, 1)
		httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if req.Method != http.MethodPut {
				t.Fatalf("request method = %s, want PUT", req.Method)
			}
			var body SessionDescription
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			member := body.Members["me"]
			if member == nil || member.Properties == nil || member.Properties.System == nil {
				t.Fatalf("member system properties missing: %+v", body.Members["me"])
			}
			if attempts == 1 {
				return nil, transientErr
			}
			if got := member.Properties.System.Connection; got != connectionID2 {
				t.Fatalf("connection ID = %s, want %s", got, connectionID2)
			}
			header := make(http.Header)
			header.Set("ETag", `"etag"`)
			select {
			case repaired <- struct{}{}:
			default:
			}
			return testResponse(req, http.StatusOK, header, []byte(`{}`)), nil
		})}
	)

	client := &Client{
		client: httpClient,
		sub:    &fakeSubscriber{subscription: &rta.Subscription{}},
		log:    slogDiscard(),
		wait:   func(context.Context) error { return nil },
		decode: func(*rta.Subscription) (*subscriptionData, error) {
			return &subscriptionData{ConnectionID: connectionID2}, nil
		},
		sessions: map[string]*Session{},
	}
	client.subscription = &rta.Subscription{}
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	session := testSession(ref, client, SessionDescription{})
	session.log = slogDiscard()
	session.etag = `"etag"`
	client.sessions[ref.URL().String()] = session
	go client.retryReconcileSessionConnection(session, connectionID1, client.backgroundSeq.Load())

	select {
	case <-repaired:
	case <-time.After(time.Second * 5):
		t.Fatal("timed out waiting for retry repair")
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d, want at least 2", attempts)
	}
}

func TestSubscriptionHandlerHandleReconnectErrorClearsSubscription(t *testing.T) {
	client := &Client{
		log:      slogDiscard(),
		sessions: map[string]*Session{},
	}
	client.subscription = &rta.Subscription{}
	client.subscriptionData = &subscriptionData{ConnectionID: uuid.New()}
	client.unsub = &fakeUnsubscriber{}
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	session := testSession(ref, client, SessionDescription{})
	client.sessions[ref.URL().String()] = session
	handler := &subscriptionHandler{
		Client: client,
		log:    slogDiscard(),
	}

	handler.HandleReconnect(errors.New("resubscribe failed"))

	client.subscriptionMu.Lock()
	defer client.subscriptionMu.Unlock()
	if client.subscription != nil {
		t.Fatal("subscription was not cleared after reconnect failure")
	}
	if client.subscriptionData != nil {
		t.Fatal("subscription data was not cleared after reconnect failure")
	}
	if session.isClosed() {
		t.Fatal("tracked session was closed after reconnect failure")
	}
	if _, ok := client.sessions[ref.URL().String()]; ok {
		t.Fatal("tracked session was not removed after reconnect failure")
	}
}

func TestSubscriptionHandlerHandleReconnectSuccessDecodeErrorClearsSubscription(t *testing.T) {
	client := &Client{
		log: slogDiscard(),
		decode: func(*rta.Subscription) (*subscriptionData, error) {
			return nil, errors.New("bad custom data")
		},
		sessions: map[string]*Session{},
	}
	subscription := &rta.Subscription{}
	client.subscription = subscription
	client.subscriptionData = &subscriptionData{ConnectionID: uuid.New()}
	client.unsub = &fakeUnsubscriber{}
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	session := testSession(ref, client, SessionDescription{})
	client.sessions[ref.URL().String()] = session
	handler := &subscriptionHandler{
		Client: client,
		log:    slogDiscard(),
	}

	handler.handleReconnectSuccess()

	client.subscriptionMu.Lock()
	defer client.subscriptionMu.Unlock()
	if client.subscription != nil {
		t.Fatal("subscription was not cleared after decode failure")
	}
	if client.subscriptionData != nil {
		t.Fatal("subscription data was not cleared after decode failure")
	}
	if session.isClosed() {
		t.Fatal("tracked session was closed after decode failure")
	}
	if _, ok := client.sessions[ref.URL().String()]; ok {
		t.Fatal("tracked session was not removed after decode failure")
	}
}

func TestSubscriptionHandlerHandleReconnectErrorForStaleSubscriptionStartsRefreshWave(t *testing.T) {
	oldConnectionID := uuid.New()
	newConnectionID := uuid.New()
	oldSubscription := &rta.Subscription{}
	newSubscription := &rta.Subscription{}
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	refreshed := make(chan struct{}, 1)
	client := &Client{
		log:      slogDiscard(),
		sessions: map[string]*Session{},
		decode: func(subscription *rta.Subscription) (*subscriptionData, error) {
			switch subscription {
			case oldSubscription:
				return &subscriptionData{ConnectionID: oldConnectionID}, nil
			case newSubscription:
				return &subscriptionData{ConnectionID: newConnectionID}, nil
			default:
				return nil, errors.New("unexpected subscription")
			}
		},
	}
	client.subscription = newSubscription
	client.subscriptionData = &subscriptionData{ConnectionID: newConnectionID}
	client.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("request method = %s, want PUT", req.Method)
		}
		var body SessionDescription
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		member := body.Members["me"]
		if member == nil || member.Properties == nil || member.Properties.System == nil {
			t.Fatalf("member system properties missing: %+v", member)
		}
		if got := member.Properties.System.Connection; got != newConnectionID {
			t.Fatalf("connection ID = %s, want %s", got, newConnectionID)
		}
		header := make(http.Header)
		header.Set("ETag", `"etag"`)
		select {
		case refreshed <- struct{}{}:
		default:
		}
		return testResponse(req, http.StatusOK, header, []byte(`{}`)), nil
	})}

	session := testSession(ref, client, SessionDescription{})
	session.log = slogDiscard()
	session.etag = `"etag"`
	client.sessions[ref.URL().String()] = session

	handler := &subscriptionHandler{
		Client:       client,
		subscription: oldSubscription,
		log:          slogDiscard(),
	}
	handler.HandleReconnect(errors.New("resubscribe failed"))

	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for refresh wave from replacement subscription")
	}
	if session.TrackingLost() {
		t.Fatal("session reported tracking loss after replacement subscription refresh")
	}
	client.sessionsMu.RLock()
	tracked := client.sessions[ref.URL().String()]
	client.sessionsMu.RUnlock()
	if tracked != session {
		t.Fatal("session was not kept tracked after replacement subscription refresh")
	}
}

func TestStartRefreshWaveIfCurrentSkipsStaleReplacementAfterLoss(t *testing.T) {
	subscription := &rta.Subscription{}
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	var requests atomic.Int32
	client := &Client{
		log:      slogDiscard(),
		sessions: map[string]*Session{},
	}
	client.subscription = subscription
	client.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		header := make(http.Header)
		header.Set("ETag", `"etag"`)
		return testResponse(req, http.StatusOK, header, []byte(`{}`)), nil
	})}

	session := testSession(ref, client, SessionDescription{})
	session.log = slogDiscard()
	session.etag = `"etag"`
	session.backgroundSeq = 0
	client.sessions[ref.URL().String()] = session

	client.subscriptionMu.Lock()
	lossSeq := client.handleSubscriptionLossLocked(nil)
	client.subscriptionMu.Unlock()
	client.shutdownTrackedSessions(lossSeq)

	if started := client.startRefreshWaveIfCurrent(subscription, 0, uuid.New()); started {
		t.Fatal("refresh wave started after subscription loss invalidated the replacement")
	}
	if requests.Load() != 0 {
		t.Fatalf("refresh requests = %d, want 0", requests.Load())
	}
	if !session.TrackingLost() {
		t.Fatal("session did not report tracking loss after subscription loss")
	}
}

func TestSubscriptionLossPreservesExplicitClose(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodPut {
			t.Fatalf("request method = %s, want PUT", req.Method)
		}
		return testResponse(req, http.StatusNoContent, nil, nil), nil
	})}

	client := &Client{
		client:   httpClient,
		log:      slogDiscard(),
		sessions: map[string]*Session{},
	}
	client.subscription = &rta.Subscription{}
	client.subscriptionData = &subscriptionData{ConnectionID: uuid.New()}

	session := testSession(ref, client, SessionDescription{})
	client.sessions[ref.URL().String()] = session
	handler := &subscriptionHandler{
		Client: client,
		log:    slogDiscard(),
	}

	handler.HandleReconnect(errors.New("resubscribe failed"))

	if session.isClosed() {
		t.Fatal("session should remain explicitly closable after subscription loss")
	}
	if err := session.CloseContext(context.Background()); err != nil {
		t.Fatalf("CloseContext returned error: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if !session.isClosed() {
		t.Fatal("session was not closed after explicit CloseContext")
	}
}

func TestTrackSessionReplacementMarksDisplacedHandleTrackingLost(t *testing.T) {
	client := &Client{
		sessions: map[string]*Session{},
	}
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	first := testSession(ref, client, SessionDescription{})
	if !client.trackSession(first, client.backgroundSeq.Load()) {
		t.Fatal("first session was not tracked")
	}
	if first.TrackingLost() {
		t.Fatal("first session reported tracking loss immediately after initial track")
	}

	second := testSession(ref, client, SessionDescription{})
	if !client.trackSession(second, client.backgroundSeq.Load()) {
		t.Fatal("replacement session was not tracked")
	}

	client.sessionsMu.RLock()
	tracked := client.sessions[ref.URL().String()]
	client.sessionsMu.RUnlock()
	if tracked != second {
		t.Fatalf("tracked session = %p, want replacement %p", tracked, second)
	}
	if !first.TrackingLost() {
		t.Fatal("displaced session did not report tracking loss after replacement")
	}
	if second.TrackingLost() {
		t.Fatal("replacement session reported tracking loss")
	}

	close(first.closed)
	close(second.closed)
}

func TestPublishTracksSessionBeforeImmediateReconcileCompletes(t *testing.T) {
	initialConnectionID := uuid.New()
	refreshedConnectionID := uuid.New()
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	var (
		requests          atomic.Int32
		currentConnection atomic.Value
		reconcileStarted  = make(chan struct{}, 1)
		reconcileRelease  = make(chan struct{})
	)
	currentConnection.Store(initialConnectionID)
	client := &Client{
		sub:      &fakeSubscriber{subscription: &rta.Subscription{}},
		userInfo: xsts.UserInfo{XUID: "1"},
		log:      slogDiscard(),
		sessions: map[string]*Session{},
	}
	client.subscription = &rta.Subscription{}
	client.active = func(*rta.Subscription) bool { return true }
	client.decode = func(*rta.Subscription) (*subscriptionData, error) {
		return &subscriptionData{ConnectionID: currentConnection.Load().(uuid.UUID)}, nil
	}
	client.wait = func(context.Context) error {
		return nil
	}
	client.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		switch {
		case req.Method == http.MethodPut && req.Header.Get("If-None-Match") == "*":
			currentConnection.Store(refreshedConnectionID)
			header := make(http.Header)
			header.Set("ETag", `"etag"`)
			return testResponse(req, http.StatusCreated, header, []byte(`{}`)), nil
		case req.Method == http.MethodPost:
			return testResponse(req, http.StatusOK, nil, nil), nil
		case req.Method == http.MethodPut && req.Header.Get("If-Match") != "":
			select {
			case reconcileStarted <- struct{}{}:
			default:
			}
			<-reconcileRelease
			header := make(http.Header)
			header.Set("ETag", `"etag"`)
			return testResponse(req, http.StatusOK, header, []byte(`{}`)), nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requests.Load(), req.Method, req.URL)
			return nil, nil
		}
	})}

	publishDone := make(chan struct{})
	var (
		session *Session
		err     error
	)
	go func() {
		session, err = client.Publish(context.Background(), ref, PublishConfig{})
		close(publishDone)
	}()

	select {
	case <-reconcileStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for immediate reconcile to start")
	}

	client.sessionsMu.RLock()
	tracked := client.sessions[ref.URL().String()]
	client.sessionsMu.RUnlock()
	if tracked == nil {
		t.Fatal("session was not tracked while immediate reconcile was in flight")
	}

	close(reconcileRelease)

	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("Publish did not finish after reconcile was released")
	}
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if session == nil {
		t.Fatal("Publish returned nil session")
	}
	if session.TrackingLost() {
		t.Fatal("session reported tracking loss after successful immediate reconcile")
	}
	close(session.closed)
}

func TestPublishReturnsTrackingLostSessionWhenGenerationChangesBeforeInitialTrack(t *testing.T) {
	initialConnectionID := uuid.New()
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	var currentConnection atomic.Value
	currentConnection.Store(initialConnectionID)
	client := &Client{
		sub:      &fakeSubscriber{subscription: &rta.Subscription{}},
		userInfo: xsts.UserInfo{XUID: "1"},
		log:      slogDiscard(),
		sessions: map[string]*Session{},
	}
	client.subscription = &rta.Subscription{}
	client.active = func(*rta.Subscription) bool { return true }
	client.decode = func(*rta.Subscription) (*subscriptionData, error) {
		return &subscriptionData{ConnectionID: currentConnection.Load().(uuid.UUID)}, nil
	}
	client.wait = func(context.Context) error {
		return nil
	}
	client.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPut && req.Header.Get("If-None-Match") == "*":
			header := make(http.Header)
			header.Set("ETag", `"etag"`)
			return testResponse(req, http.StatusCreated, header, []byte(`{}`)), nil
		case req.Method == http.MethodPost:
			client.backgroundSeq.Add(1)
			return testResponse(req, http.StatusOK, nil, nil), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
			return nil, nil
		}
	})}

	session, err := client.Publish(context.Background(), ref, PublishConfig{})
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if session == nil {
		t.Fatal("Publish returned nil session")
	}

	client.sessionsMu.RLock()
	tracked := client.sessions[ref.URL().String()]
	client.sessionsMu.RUnlock()
	if tracked != nil {
		t.Fatal("session was tracked after generation changed before initial attach")
	}
	if !session.TrackingLost() {
		t.Fatal("session did not report tracking loss after generation changed before initial attach")
	}
	close(session.closed)
}

func TestRetryReconcileSessionConnectionMarksTrackingLostWhenLateAttachFails(t *testing.T) {
	initialConnectionID := uuid.New()
	refreshedConnectionID := uuid.New()
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	var reconcileRequests atomic.Int32
	requestDone := make(chan struct{}, 1)

	client := &Client{
		sub:      &fakeSubscriber{subscription: &rta.Subscription{}},
		log:      slogDiscard(),
		sessions: map[string]*Session{},
	}
	client.subscription = &rta.Subscription{}
	client.active = func(*rta.Subscription) bool { return true }
	client.decode = func(*rta.Subscription) (*subscriptionData, error) {
		return &subscriptionData{ConnectionID: refreshedConnectionID}, nil
	}
	client.wait = func(context.Context) error {
		return nil
	}
	client.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut || req.Header.Get("If-Match") == "" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		if reconcileRequests.Add(1) == 1 {
			client.backgroundSeq.Add(1)
		}
		header := make(http.Header)
		header.Set("ETag", `"etag"`)
		select {
		case requestDone <- struct{}{}:
		default:
		}
		return testResponse(req, http.StatusOK, header, []byte(`{}`)), nil
	})}

	session := testSession(ref, client, SessionDescription{})
	session.log = slogDiscard()
	session.etag = `"etag"`

	go client.retryReconcileSessionConnection(session, initialConnectionID, client.backgroundSeq.Load())

	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconcile retry request")
	}

	deadline := time.After(time.Second)
	for {
		client.sessionsMu.RLock()
		tracked := client.sessions[ref.URL().String()]
		client.sessionsMu.RUnlock()
		if tracked == nil && session.TrackingLost() {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("session state did not reflect tracking loss: tracked=%v trackingLost=%v", tracked != nil, session.TrackingLost())
		default:
			time.Sleep(time.Millisecond * 10)
		}
	}
	if reconcileRequests.Load() != 1 {
		t.Fatalf("reconcile requests = %d, want 1", reconcileRequests.Load())
	}
	close(session.closed)
}

func TestRetryReconcileSessionConnectionDoesNotStealTrackingFromReplacementHandle(t *testing.T) {
	initialConnectionID := uuid.New()
	refreshedConnectionID := uuid.New()
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	reconcileStarted := make(chan struct{}, 1)
	reconcileRelease := make(chan struct{})
	done := make(chan struct{})

	client := &Client{
		log:      slogDiscard(),
		sessions: map[string]*Session{},
		decode: func(*rta.Subscription) (*subscriptionData, error) {
			return &subscriptionData{ConnectionID: refreshedConnectionID}, nil
		},
		active: func(*rta.Subscription) bool { return true },
	}
	client.subscription = &rta.Subscription{}
	client.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut || req.Header.Get("If-Match") == "" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		select {
		case reconcileStarted <- struct{}{}:
		default:
		}
		<-reconcileRelease
		header := make(http.Header)
		header.Set("ETag", `"etag"`)
		return testResponse(req, http.StatusOK, header, []byte(`{}`)), nil
	})}

	oldSession := testSession(ref, client, SessionDescription{})
	oldSession.log = slogDiscard()
	oldSession.etag = `"etag"`
	if !client.trackSession(oldSession, client.backgroundSeq.Load()) {
		t.Fatal("old session was not initially tracked")
	}

	go func() {
		client.retryReconcileSessionConnection(oldSession, initialConnectionID, client.backgroundSeq.Load())
		close(done)
	}()

	select {
	case <-reconcileStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for late reconcile retry to start")
	}

	newSession := testSession(ref, client, SessionDescription{})
	newSession.log = slogDiscard()
	if !client.trackSession(newSession, client.backgroundSeq.Load()) {
		t.Fatal("replacement session was not tracked")
	}

	close(reconcileRelease)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retryReconcileSessionConnection did not finish")
	}

	client.sessionsMu.RLock()
	tracked := client.sessions[ref.URL().String()]
	client.sessionsMu.RUnlock()
	if tracked != newSession {
		t.Fatalf("tracked session = %p, want replacement %p", tracked, newSession)
	}
	if !oldSession.TrackingLost() {
		t.Fatal("old session did not report tracking loss after replacement took over")
	}
	if newSession.TrackingLost() {
		t.Fatal("replacement session reported tracking loss")
	}
	close(oldSession.closed)
	close(newSession.closed)
}

func TestRetryReconcileSessionConnectionMarksTrackingLostWhenGenerationChangesBeforeRetryStarts(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	client := &Client{
		log:      slogDiscard(),
		sessions: map[string]*Session{},
	}
	session := testSession(ref, client, SessionDescription{})
	session.log = slogDiscard()
	backgroundSeq := client.backgroundSeq.Load()
	client.backgroundSeq.Add(1)

	done := make(chan struct{})
	go func() {
		client.retryReconcileSessionConnection(session, uuid.New(), backgroundSeq)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retryReconcileSessionConnection did not finish")
	}
	if !session.TrackingLost() {
		t.Fatal("session did not report tracking loss after generation changed before retry")
	}
	client.sessionsMu.RLock()
	_, tracked := client.sessions[ref.URL().String()]
	client.sessionsMu.RUnlock()
	if tracked {
		t.Fatal("session was tracked after generation changed before retry")
	}
	close(session.closed)
}

func TestRetryReconcileSessionConnectionMarksTrackingLostAfterRetryExhaustion(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	var attempts atomic.Int32
	client := &Client{
		sub:      &fakeSubscriber{subscription: &rta.Subscription{}},
		log:      slogDiscard(),
		sessions: map[string]*Session{},
	}
	client.subscription = &rta.Subscription{}
	client.active = func(*rta.Subscription) bool { return true }
	client.decode = func(*rta.Subscription) (*subscriptionData, error) {
		return &subscriptionData{ConnectionID: uuid.New()}, nil
	}
	client.wait = func(context.Context) error {
		return nil
	}
	client.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts.Add(1)
		return nil, errors.New("reconcile failed")
	})}

	session := testSession(ref, client, SessionDescription{})
	session.log = slogDiscard()
	session.etag = `"etag"`

	done := make(chan struct{})
	go func() {
		client.retryReconcileSessionConnection(session, uuid.New(), client.backgroundSeq.Load())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retryReconcileSessionConnection did not finish after retry exhaustion")
	}
	if attempts.Load() != 5 {
		t.Fatalf("reconcile attempts = %d, want 5", attempts.Load())
	}
	if !session.TrackingLost() {
		t.Fatal("session did not report tracking loss after retry exhaustion")
	}
	client.sessionsMu.RLock()
	_, tracked := client.sessions[ref.URL().String()]
	client.sessionsMu.RUnlock()
	if tracked {
		t.Fatal("session was tracked after retry exhaustion")
	}
	close(session.closed)
}

var slogDiscard = testutil.SlogDiscard
