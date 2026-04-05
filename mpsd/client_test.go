package mpsd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/rta"
	"github.com/df-mc/go-xsapi/xal/xsts"
	"github.com/google/uuid"
)

type fakeUnsubscriber struct {
	attempts int
	failures int
}

func (f *fakeUnsubscriber) Unsubscribe(context.Context, *rta.Subscription) error {
	f.attempts++
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

func TestClientRetryReconcileSessionConnectionRepairsSession(t *testing.T) {
	connectionID1 := uuid.New()
	connectionID2 := uuid.New()

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
				return nil, errSubscriptionUnavailable
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

func TestSubscriptionHandlerHandleReconnectErrorRebuildsSubscription(t *testing.T) {
	connectionID := uuid.New()
	sub := &fakeSubscriber{
		subscription: &rta.Subscription{},
		called:       make(chan struct{}, 1),
	}
	client := &Client{
		sub:  sub,
		log:  slogDiscard(),
		wait: func(context.Context) error { return nil },
		decode: func(*rta.Subscription) (*subscriptionData, error) {
			return &subscriptionData{ConnectionID: connectionID}, nil
		},
	}
	client.subscription = &rta.Subscription{}
	client.subscriptionData = &subscriptionData{ConnectionID: uuid.New()}
	client.unsub = &fakeUnsubscriber{}
	handler := &subscriptionHandler{
		Client: client,
		log:    slogDiscard(),
	}

	handler.HandleReconnect(errors.New("resubscribe failed"))

	select {
	case <-sub.called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for MPSD subscription rebuild")
	}

	deadline := time.After(time.Second)
	for {
		client.subscriptionMu.Lock()
		subscription := client.subscription
		data := client.subscriptionData
		client.subscriptionMu.Unlock()

		if subscription != nil && data != nil && data.ConnectionID == connectionID {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("subscription rebuild did not complete: subscription=%v subscriptionData=%+v", subscription != nil, data)
		default:
			time.Sleep(time.Millisecond * 10)
		}
	}
}

func TestSubscriptionHandlerHandleReconnectErrorRetriesSubscriptionRepair(t *testing.T) {
	connectionID := uuid.New()
	sub := &fakeSubscriber{
		subscription: &rta.Subscription{},
		called:       make(chan struct{}, 4),
		failures:     1,
	}
	client := &Client{
		sub:  sub,
		log:  slogDiscard(),
		wait: func(context.Context) error { return nil },
		decode: func(*rta.Subscription) (*subscriptionData, error) {
			return &subscriptionData{ConnectionID: connectionID}, nil
		},
	}
	client.subscription = &rta.Subscription{}
	client.subscriptionData = &subscriptionData{ConnectionID: uuid.New()}
	client.unsub = &fakeUnsubscriber{}
	handler := &subscriptionHandler{
		Client: client,
		log:    slogDiscard(),
	}

	handler.HandleReconnect(errors.New("resubscribe failed"))

	deadline := time.After(time.Second * 3)
	retries := 0
	for {
		select {
		case <-sub.called:
			retries++
		default:
		}
		client.subscriptionMu.Lock()
		data := client.subscriptionData
		client.subscriptionMu.Unlock()
		if retries >= 2 && data != nil && data.ConnectionID == connectionID {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("subscription repair did not retry successfully: retries=%d subscriptionData=%+v", retries, data)
		default:
			time.Sleep(time.Millisecond * 10)
		}
	}
}

func TestPublishReturnsSessionWhenImmediateLateReconcileFails(t *testing.T) {
	initialConnectionID := uuid.New()
	refreshedConnectionID := uuid.New()

	var (
		requests          atomic.Int32
		currentConnection atomic.Value
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
		if requests.Load() >= 2 {
			return errSubscriptionUnavailable
		}
		return nil
	}
	client.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch requests.Add(1) {
		case 1:
			currentConnection.Store(refreshedConnectionID)
			header := make(http.Header)
			header.Set("ETag", `"etag"`)
			return testResponse(req, http.StatusCreated, header, []byte(`{}`)), nil
		case 2:
			return testResponse(req, http.StatusCreated, nil, []byte(`{}`)), nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requests.Load(), req.Method, req.URL)
			return nil, nil
		}
	})}

	session, err := client.Publish(context.Background(), SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}, PublishConfig{})
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}
	if session == nil {
		t.Fatal("Publish returned nil session")
	}
	close(session.closed)
}

func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
