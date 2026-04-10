package social

import (
	"context"
	"errors"
	"testing"

	"github.com/df-mc/go-xsapi/v2/rta"
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
