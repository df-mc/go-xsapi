package mpsd

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

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
