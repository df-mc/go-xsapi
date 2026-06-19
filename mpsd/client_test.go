package mpsd

import (
	"context"
	"testing"

	"github.com/df-mc/go-xsapi/v2/rta"
)

func TestClientCloseContextSkipsInactiveSubscription(t *testing.T) {
	c := &Client{
		subscription: rta.NewSubscription(resourceURI, rta.NopSubscriptionHandler{}),
	}

	if err := c.CloseContext(context.Background()); err != nil {
		t.Fatalf("CloseContext returned error: %v", err)
	}
}
