package social

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

func TestSubscribeWithoutRTAFails(t *testing.T) {
	client := New(http.DefaultClient, nil, xsts.UserInfo{XUID: "1"}, nil)

	err := client.Subscribe(context.Background(), NopSubscriptionHandler{})
	if !errors.Is(err, rta.ErrUnavailable) {
		t.Fatalf("Subscribe error = %v, want %v", err, rta.ErrUnavailable)
	}
}

func TestSubscriptionHandlerAllowsNonComparableHandlers(t *testing.T) {
	calls := make(chan string, 1)
	handler := nonComparableSocialHandler{
		calls: calls,
		data:  []string{"non-comparable"},
	}
	c := &Client{
		subscriptionHandlers: []SubscriptionHandler{handler},
	}
	h := &subscriptionHandler{
		Client: c,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	h.HandleEvent(json.RawMessage(`{"NotificationType":"Added","Xuids":["1","2"]}`))

	select {
	case got := <-calls:
		if got != "Added:1,2" {
			t.Fatalf("handler call = %q, want %q", got, "Added:1,2")
		}
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}
}

func TestSubscriptionHandlerIgnoresUserUnsubscribe(t *testing.T) {
	calls := make(chan string, 1)
	handler := nonComparableSocialHandler{
		calls: calls,
		data:  []string{"non-comparable"},
	}
	c := &Client{
		subscriptionHandlers: []SubscriptionHandler{handler},
	}
	h := &subscriptionHandler{
		Client: c,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	h.HandleError(rta.ErrUnsubscribed)

	select {
	case got := <-calls:
		t.Fatalf("handler call = %q, want none", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSubscriptionHandlerNotifiesSubscriptionLost(t *testing.T) {
	calls := make(chan string, 1)
	handler := nonComparableSocialHandler{
		calls: calls,
		data:  []string{"non-comparable"},
	}
	c := &Client{
		subscriptionHandlers: []SubscriptionHandler{handler},
	}
	h := &subscriptionHandler{
		Client: c,
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	h.HandleError(io.ErrUnexpectedEOF)

	select {
	case got := <-calls:
		if got != "lost" {
			t.Fatalf("handler call = %q, want lost", got)
		}
	case <-time.After(time.Second):
		t.Fatal("subscription lost handler was not called")
	}
}

type nonComparableSocialHandler struct {
	calls chan<- string
	data  []string
}

func (h nonComparableSocialHandler) HandleSocialNotification(typ string, xuids []string) {
	h.calls <- typ + ":" + strings.Join(xuids, ",")
}

func (h nonComparableSocialHandler) HandleIncomingFriendRequestCountChange(int) {}

func (h nonComparableSocialHandler) HandleSubscriptionLost() {
	h.calls <- "lost"
}
