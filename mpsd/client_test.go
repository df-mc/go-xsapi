package mpsd

import (
	"context"
	"errors"
	"testing"

	"github.com/df-mc/go-xsapi/rta"
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
