package social

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/rta"
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

func TestClientCloseContextDoesNotBlockOnRecoverySubscribe(t *testing.T) {
	sub := &blockingSubscriber{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	client := &Client{
		sub:                  sub,
		subscriptionHandlers: []SubscriptionHandler{NopSubscriptionHandler{}},
		recovering:           true,
	}

	done := make(chan struct{})
	go func() {
		client.retryRecoverSubscription(client.subscriptionSeq.Load())
		close(done)
	}()

	select {
	case <-sub.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovery subscribe attempt")
	}

	closed := make(chan error, 1)
	go func() {
		closed <- client.CloseContext(context.Background())
	}()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("CloseContext returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseContext blocked on background recovery subscribe")
	}

	close(sub.release)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery goroutine did not exit after in-flight subscribe finished")
	}

	if client.subscription != nil {
		t.Fatal("recovery installed a subscription after CloseContext returned")
	}
}

func TestClientSubscribeSharesInFlightRecoverySubscription(t *testing.T) {
	sub := &blockingSubscriber{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	client := &Client{
		sub:                  sub,
		subscriptionHandlers: []SubscriptionHandler{NopSubscriptionHandler{}},
		recovering:           true,
	}

	done := make(chan struct{})
	go func() {
		client.retryRecoverSubscription(client.subscriptionSeq.Load())
		close(done)
	}()

	select {
	case <-sub.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for background recovery subscribe")
	}

	subscribeErr := make(chan error, 1)
	go func() {
		subscribeErr <- client.Subscribe(context.Background(), NopSubscriptionHandler{})
	}()

	select {
	case <-sub.started:
		t.Fatal("manual Subscribe started a second concurrent subscribe instead of sharing recovery")
	case <-time.After(100 * time.Millisecond):
	}

	close(sub.release)

	select {
	case err := <-subscribeErr:
		if err != nil {
			t.Fatalf("Subscribe returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for manual Subscribe to complete")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovery goroutine to complete")
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
		if err == nil {
			t.Fatal("expected Subscribe to fail after close invalidated the in-flight install")
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

func TestClientSubscribeDoesNotHoldLockDuringDiscardCleanup(t *testing.T) {
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
	case <-unsub.called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for discarded subscription cleanup")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- client.Subscribe(context.Background(), NopSubscriptionHandler{})
	}()

	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second Subscribe returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Subscribe blocked behind cleanup unsubscribe")
	}

	close(unsub.release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first Subscribe returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first Subscribe did not finish after cleanup unblocked")
	}
}
