package mpsd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
