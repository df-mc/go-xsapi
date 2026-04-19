package social

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/internal/testutil"
	"github.com/df-mc/go-xsapi/v2/rta"
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
}

func (f *fakeSubscriber) Subscribe(context.Context, string) (*rta.Subscription, error) {
	f.attempts++
	if f.err != nil {
		return nil, f.err
	}
	if f.subscription == nil {
		f.subscription = &rta.Subscription{}
	}
	return f.subscription, nil
}

type blockingSubscriber struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingSubscriber) Subscribe(context.Context, string) (*rta.Subscription, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-b.release
	return &rta.Subscription{}, nil
}

func TestClientCloseContextKeepsSubscriptionStateOnUnsubscribeError(t *testing.T) {
	subscription := &rta.Subscription{}
	handlers := []SubscriptionHandler{NopSubscriptionHandler{}}
	unsub := &fakeUnsubscriber{failures: 1}
	client := &Client{
		subscription:         subscription,
		subscriptionHandlers: handlers,
		unsub:                unsub,
	}

	if err := client.CloseContext(context.Background()); err == nil {
		t.Fatal("expected unsubscribe error")
	}
	if client.subscription != subscription {
		t.Fatal("subscription was cleared after unsubscribe failure")
	}
	if len(client.subscriptionHandlers) != 1 {
		t.Fatalf("handlers length = %d, want 1 after unsubscribe failure", len(client.subscriptionHandlers))
	}

	if err := client.CloseContext(context.Background()); err != nil {
		t.Fatalf("retry close returned error: %v", err)
	}
	if client.subscription != nil {
		t.Fatal("subscription was not cleared after successful retry")
	}
	if client.subscriptionHandlers != nil {
		t.Fatal("handlers were not cleared after successful retry")
	}
	if unsub.attempts != 2 {
		t.Fatalf("unsubscribe attempts = %d, want 2", unsub.attempts)
	}
}

func TestClientCloseContextClearsHandlersWithoutSubscription(t *testing.T) {
	client := &Client{
		subscriptionHandlers: []SubscriptionHandler{NopSubscriptionHandler{}},
	}

	if err := client.CloseContext(context.Background()); err != nil {
		t.Fatalf("CloseContext returned error: %v", err)
	}
	if client.subscriptionHandlers != nil {
		t.Fatal("handlers were not cleared when closing without a live subscription")
	}
}

func TestClientCloseContextSerializesConcurrentCalls(t *testing.T) {
	unsub := &fakeUnsubscriber{
		called:  make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	client := &Client{
		subscription:         &rta.Subscription{},
		subscriptionHandlers: []SubscriptionHandler{NopSubscriptionHandler{}},
		unsub:                unsub,
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

func TestClientSubscribeDoesNotRegisterHandlerWhenInitialSubscribeFails(t *testing.T) {
	handler := NopSubscriptionHandler{}
	sub := &fakeSubscriber{err: errors.New("subscribe failed")}
	client := &Client{sub: sub}

	if err := client.Subscribe(context.Background(), handler); err == nil {
		t.Fatal("expected subscribe error")
	}
	if sub.attempts != 1 {
		t.Fatalf("subscribe attempts = %d, want 1", sub.attempts)
	}
	if client.subscriptionHandlers != nil {
		t.Fatal("handler was registered even though subscribe failed")
	}
}

func TestClientSubscribeReturnsUnavailableWithoutSubscriber(t *testing.T) {
	client := &Client{}

	err := client.Subscribe(context.Background(), NopSubscriptionHandler{})
	if !errors.Is(err, errSubscriptionUnavailable) {
		t.Fatalf("Subscribe error = %v, want %v", err, errSubscriptionUnavailable)
	}
}

func TestSubscriptionHandlerHandleReconnectErrorPreservesHandlers(t *testing.T) {
	client := &Client{
		subscriptionHandlers: []SubscriptionHandler{NopSubscriptionHandler{}},
		subscription:         &rta.Subscription{},
		log:                  slogDiscard(),
	}
	handler := &subscriptionHandler{Client: client}

	handler.HandleReconnect(errors.New("reconnect failed"))

	client.subscriptionMu.RLock()
	defer client.subscriptionMu.RUnlock()
	if client.subscription != nil {
		t.Fatal("subscription was not cleared after reconnect failure")
	}
	if len(client.subscriptionHandlers) != 1 {
		t.Fatalf("handlers length = %d, want 1 after reconnect failure", len(client.subscriptionHandlers))
	}
}

func TestClientSubscribeDuplicatesComparableHandlerWithLiveSubscription(t *testing.T) {
	handler := NopSubscriptionHandler{}
	client := &Client{
		sub:                  &fakeSubscriber{},
		subscription:         &rta.Subscription{},
		subscriptionHandlers: []SubscriptionHandler{handler},
	}

	if err := client.Subscribe(context.Background(), handler); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if len(client.subscriptionHandlers) != 2 {
		t.Fatalf("handlers length = %d, want 2", len(client.subscriptionHandlers))
	}
}

func TestClientSubscribeAfterReconnectLossRemainsAppendOnly(t *testing.T) {
	handler := &pointerHandler{id: "A"}
	client := &Client{
		sub: &fakeSubscriber{},
		log: slogDiscard(),
	}
	h := &subscriptionHandler{Client: client}
	client.subscription = &rta.Subscription{}
	client.subscriptionHandlers = []SubscriptionHandler{handler}

	h.HandleReconnect(errors.New("reconnect failed"))

	if err := client.Subscribe(context.Background(), handler); err != nil {
		t.Fatalf("Subscribe returned error: %v", err)
	}
	if len(client.subscriptionHandlers) != 2 {
		t.Fatalf("handlers length = %d, want 2 after recovery subscribe", len(client.subscriptionHandlers))
	}
	if client.subscription == nil {
		t.Fatal("subscription was not re-established after reconnect loss")
	}
	if err := client.Subscribe(context.Background(), handler); err != nil {
		t.Fatalf("second Subscribe returned error: %v", err)
	}
	if len(client.subscriptionHandlers) != 3 {
		t.Fatalf("handlers length = %d, want 3 after second append-only subscribe", len(client.subscriptionHandlers))
	}
}

func TestClientSubscribeAfterReconnectLossDoesNotCollapseConcurrentNewComparableHandlers(t *testing.T) {
	sub := &blockingSubscriber{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	existing := namedHandler("existing")
	newHandler := namedHandler("new")
	client := &Client{
		sub:                  sub,
		log:                  slogDiscard(),
		subscriptionHandlers: []SubscriptionHandler{existing},
		subscription:         &rta.Subscription{},
	}
	h := &subscriptionHandler{Client: client}

	h.HandleReconnect(errors.New("reconnect failed"))

	errs := make(chan error, 2)
	go func() { errs <- client.Subscribe(context.Background(), newHandler) }()
	go func() { errs <- client.Subscribe(context.Background(), newHandler) }()

	select {
	case <-sub.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovery subscribe attempt")
	}

	close(sub.release)

	for range 2 {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("Subscribe returned error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for concurrent Subscribe calls")
		}
	}

	if len(client.subscriptionHandlers) != 3 {
		t.Fatalf("handlers length = %d, want 3 after concurrent recovery subscribes", len(client.subscriptionHandlers))
	}
	if client.subscription == nil {
		t.Fatal("subscription was not re-established after reconnect loss")
	}
}

func TestClientCloseContextPreventsInflightSubscribeInstall(t *testing.T) {
	sub := &blockingSubscriber{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	unsub := &fakeUnsubscriber{called: make(chan struct{}, 1)}
	client := &Client{
		sub:   sub,
		unsub: unsub,
	}

	subscribeErr := make(chan error, 1)
	go func() {
		subscribeErr <- client.Subscribe(context.Background(), NopSubscriptionHandler{})
	}()

	select {
	case <-sub.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for in-flight subscribe")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- client.CloseContext(context.Background())
	}()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("CloseContext returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseContext blocked waiting for in-flight subscribe")
	}

	close(sub.release)

	select {
	case err := <-subscribeErr:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Subscribe error = %v, want %v", err, net.ErrClosed)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Subscribe to finish")
	}

	if client.subscription != nil {
		t.Fatal("subscription was installed after CloseContext returned")
	}
	select {
	case <-unsub.called:
	case <-time.After(time.Second):
		t.Fatal("discarded in-flight subscription was not cleaned up")
	}
}

func TestClientCloseContextClearsStaleSubscribeBarrier(t *testing.T) {
	done := make(chan struct{})
	client := &Client{
		subscribeDone: done,
		subscriptionHandlers: []SubscriptionHandler{
			NopSubscriptionHandler{},
		},
	}

	if err := client.CloseContext(context.Background()); err != nil {
		t.Fatalf("CloseContext returned error: %v", err)
	}
	if client.subscribeDone != nil {
		t.Fatal("subscribeDone was not cleared on close")
	}
}

func TestClientSubscribeReturnsBeforeDiscardCleanupCompletes(t *testing.T) {
	sub := &blockingSubscriber{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	unsub := &fakeUnsubscriber{
		called:  make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	client := &Client{
		sub:   sub,
		unsub: unsub,
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- client.Subscribe(context.Background(), NopSubscriptionHandler{})
	}()

	select {
	case <-sub.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first subscribe fetch")
	}

	client.subscriptionMu.Lock()
	client.subscription = &rta.Subscription{}
	client.subscriptionMu.Unlock()

	close(sub.release)

	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first Subscribe returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe blocked behind cleanup unsubscribe")
	}

	select {
	case <-unsub.called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for discarded subscription cleanup")
	}

	close(unsub.release)
}

var slogDiscard = testutil.SlogDiscard

type namedHandler string

func (namedHandler) HandleSocialNotification(string, []string)  {}
func (namedHandler) HandleIncomingFriendRequestCountChange(int) {}

type pointerHandler struct{ id string }

func (*pointerHandler) HandleSocialNotification(string, []string)  {}
func (*pointerHandler) HandleIncomingFriendRequestCountChange(int) {}
