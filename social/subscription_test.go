package social

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/rta"
)

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

func TestAddSubscriptionHandlerDeduplicatesComparableHandlers(t *testing.T) {
	handler := &countingSocialHandler{}
	c := &Client{}

	c.addSubscriptionHandler(handler)
	c.addSubscriptionHandler(handler)

	if got := len(c.subscriptionHandlers); got != 1 {
		t.Fatalf("subscription handler count = %d, want 1", got)
	}
}

func TestAddSubscriptionHandlerAllowsNonComparableHandlers(t *testing.T) {
	handler := nonComparableSocialHandler{
		calls: make(chan string, 1),
		data:  []string{"non-comparable"},
	}
	c := &Client{}

	c.addSubscriptionHandler(handler)
	c.addSubscriptionHandler(handler)

	if got := len(c.subscriptionHandlers); got != 2 {
		t.Fatalf("subscription handler count = %d, want 2", got)
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

type countingSocialHandler struct{}

func (*countingSocialHandler) HandleSocialNotification(string, []string)  {}
func (*countingSocialHandler) HandleIncomingFriendRequestCountChange(int) {}
func (*countingSocialHandler) HandleSubscriptionLost()                    {}

var _ SubscriptionHandler = legacySocialHandler{}

type legacySocialHandler struct{}

func (legacySocialHandler) HandleSocialNotification(string, []string)  {}
func (legacySocialHandler) HandleIncomingFriendRequestCountChange(int) {}
