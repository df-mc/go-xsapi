package social

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"

	"github.com/df-mc/go-xsapi/v2/rta"
)

// Subscribe subscribes to RTA (Real-Time Activity) services to receive
// notifications for changes in the caller's friend list.
//
// The provided [SubscriptionHandler] is used to dispatch events delivered
// over the RTA subscription, such as when a user adds or removes the caller.
//
// The RTA subscription is created on the first call and cached internally
// to avoid exceeding RTA's maximum subscription limit. Subsequent calls
// reuse the existing subscription and append h to the list of active handlers.
//
// Subscribe returns an error if h is nil.
func (c *Client) Subscribe(ctx context.Context, h SubscriptionHandler) (err error) {
	if h == nil {
		return errors.New("xsapi/social: cannot subscribe with a nil SubscriptionHandler")
	}

	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()

	if err := c.rta.Subscribe(ctx, c.subscription); err != nil {
		return err
	}

	c.subscriptionHandlers = append(c.subscriptionHandlers, h)
	return nil
}

// subscriptionHandler is an internal implementation of [rta.SubscriptionHandler]
// registered on the RTA subscription for receiving events and dispatching them to all
// [SubscriptionHandler] implementations registered via [Client.Subscribe].
type subscriptionHandler struct {
	*Client
	log *slog.Logger

	rta.NopSubscriptionHandler
}

// HandleEvent handles an event received over the RTA subscription.
//
// The payload is a JSON struct describing a change in the caller's
// friend list. HandleEvent dispatches the method corresponding to
// the notification type in all [SubscriptionHandler] implementations
// registered to the Client.
func (h *subscriptionHandler) HandleEvent(custom json.RawMessage) {
	var data struct {
		// Type is the notification type of the event.
		Type string `json:"NotificationType"`

		// Count is the current number of incoming friend requests.
		// It is only populated when Type is notificationTypeIncomingFriendRequestCountChanged.
		Count *int `json:"Count"`

		// XUIDs lists the XUIDs of users affected by the change.
		// It is only populated when Type is one of the exported NotificationType* constants defined below.
		XUIDs []string `json:"Xuids"`
	}
	if err := json.Unmarshal(custom, &data); err != nil {
		h.log.Error("error decoding event payload",
			slog.String("custom", string(custom)),
			slog.Any("error", err),
		)
		return
	}

	switch data.Type {
	case notificationTypeIncomingFriendRequestCountChanged:
		if data.Count == nil {
			h.log.Error("friend request count is absent from subscription event payload",
				slog.String("custom", string(custom)),
			)
			return
		}

		for _, handler := range h.handlers() {
			go handler.HandleIncomingFriendRequestCountChange(*data.Count)
		}
		return
	case NotificationTypeAdded, NotificationTypeRemoved, NotificationTypeChanged:
		if len(data.XUIDs) == 0 {
			h.log.Error("XUIDs are absent from subscription event payload",
				slog.String("custom", string(custom)),
			)
			return
		}

		for _, handler := range h.handlers() {
			xuids := slices.Clone(data.XUIDs)
			go handler.HandleSocialNotification(data.Type, xuids)
		}
	default:
		h.log.Warn("unexpected subscription notification type",
			slog.String("type", data.Type),
		)
	}
}

func (h *subscriptionHandler) HandleError(err error) {
	if errors.Is(err, rta.ErrUnsubscribed) {
		return
	}
	h.log.Error("subscription lost", "err", err)

	for _, handler := range h.handlers() {
		go handler.HandleSubscriptionLost()
	}
}

func (h *subscriptionHandler) handlers() []SubscriptionHandler {
	h.subscriptionMu.RLock()
	defer h.subscriptionMu.RUnlock()
	return slices.Clone(h.subscriptionHandlers)
}

// SubscriptionHandler is the interface for receiving real-time notifications
// for changes in the caller's friend list via an RTA (Real-Time Activity)
// subscription.
//
// Use [Client.Subscribe] to subscribe and register an implementation.
// NopSubscriptionHandler is a no-op implementation of SubscriptionHandler.
type SubscriptionHandler interface {
	// HandleSocialNotification is called when a change in the caller's friend
	// list is delivered via the RTA subscription.
	//
	// typ is one of the NotificationType* constants defined below.
	// xuids lists the XUIDs of the users affected by the change.
	HandleSocialNotification(typ string, xuids []string)

	// HandleIncomingFriendRequestCountChange is called when the number of
	// incoming friend requests changes.
	//
	// The payload contains only the updated count; the XUIDs of the users
	// involved are not included. Therefore, it is generally used for notification purposes.
	HandleIncomingFriendRequestCountChange(count int)

	// HandleSubscriptionLost is called when the underlying subscription is lost.
	// The caller might be able to call [Client.Subscribe] again using the same handler
	// to reconnect to the RTA service.
	HandleSubscriptionLost()
}

// NopSubscriptionHandler is a no-op implementation of [SubscriptionHandler].
type NopSubscriptionHandler struct{}

func (NopSubscriptionHandler) HandleSocialNotification(string, []string)  {}
func (NopSubscriptionHandler) HandleIncomingFriendRequestCountChange(int) {}
func (NopSubscriptionHandler) HandleSubscriptionLost()                    {}

const (
	// NotificationTypeAdded is the notification type for when one or more users
	// add the caller as a friend.
	NotificationTypeAdded = "Added"

	// NotificationTypeRemoved is the notification type for when one or more
	// users are no longer friends with the caller.
	NotificationTypeRemoved = "Removed"

	// NotificationTypeChanged is the notification type for when one or more
	// users in the caller's friend list have changed.
	//
	// It is used to keep the caller's local view of their friend list
	// up to date.
	NotificationTypeChanged = "Changed"

	// notificationTypeIncomingFriendRequestCountChanged is the notification
	// type for when the number of pending friend requests sent to the caller
	// changes.
	// It is unexported since it is never delivered to the caller.
	notificationTypeIncomingFriendRequestCountChanged = "IncomingFriendRequestCountChanged"
)
