package social

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/df-mc/go-xsapi/v2/rta"
)

// errSubscriptionUnavailable is returned when no RTA subscriber is configured
// and the social subscription cannot be refreshed.
var errSubscriptionUnavailable = errors.New("social: subscription unavailable")

// Subscribe subscribes to RTA (Real-Time Activity) services to receive
// notifications for changes in the caller's friend list.
//
// The provided [SubscriptionHandler] is used to dispatch events delivered
// over the RTA subscription, such as when a user adds or removes the caller.
//
// The RTA subscription is created on the first call and cached internally
// to avoid exceeding RTA's maximum subscription limit. Subsequence calls
// reuse the existing subscription and append h to the list of active handlers.
//
// Subscribe returns an error if h is nil.
//
// A reconnect failure is terminal for the current live social subscription.
// The registered handler set is preserved so a later Subscribe call can
// re-establish the transport. Subscribe remains append-only even after
// reconnect loss, so the provided handler is registered again like any other
// call.
func (c *Client) Subscribe(ctx context.Context, h SubscriptionHandler) (err error) {
	if h == nil {
		return errors.New("xsapi/social: cannot subscribe with a nil SubscriptionHandler")
	}

	seq := c.subscriptionSeq.Load()
	for {
		if err := c.ensureSubscription(ctx, seq); err != nil {
			return err
		}
		c.subscriptionMu.Lock()
		if !c.subscriptionActive(seq) {
			c.subscriptionMu.Unlock()
			return context.Canceled
		}
		if c.subscription == nil {
			c.subscriptionMu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			continue
		}
		c.subscriptionHandlers = append(c.subscriptionHandlers, h)
		c.subscriptionMu.Unlock()
		return nil
	}
}

// subscriptionHandler is an internal implementation of [rta.SubscriptionHandler]
// registered on the RTA subscription for receiving events and dispatching them to all
// [SubscriptionHandler] implementations registered via [Client.Subscribe].
type subscriptionHandler struct {
	*Client
	subscription *rta.Subscription
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

		h.subscriptionMu.RLock()
		for _, handler := range h.subscriptionHandlers {
			go handler.HandleIncomingFriendRequestCountChange(*data.Count)
		}
		h.subscriptionMu.RUnlock()
		return
	case NotificationTypeAdded, NotificationTypeRemoved, NotificationTypeChanged:
		if len(data.XUIDs) == 0 {
			h.log.Error("XUIDs are absent from subscription event payload",
				slog.String("custom", string(custom)),
			)
			return
		}

		h.subscriptionMu.RLock()
		for _, handler := range h.subscriptionHandlers {
			xuids := slices.Clone(data.XUIDs)
			go handler.HandleSocialNotification(data.Type, xuids)
		}
		h.subscriptionMu.RUnlock()
	default:
		h.log.Warn("unexpected subscription notification type",
			slog.String("type", data.Type),
		)
	}
}

// HandleReconnect implements [rta.SubscriptionHandler.HandleReconnect].
// A reconnect failure is terminal for the current social subscription
// transport, but the registered handler set is preserved so callers may
// re-establish the transport later without losing existing registrations.
func (h *subscriptionHandler) HandleReconnect(err error) {
	if err != nil {
		h.subscriptionMu.Lock()
		if h.subscription != nil && h.subscription != h.Client.subscription {
			h.subscriptionMu.Unlock()
			return
		}
		h.Client.subscription = nil
		h.subscriptionMu.Unlock()
		h.log.Error("error reconnecting social subscription", slog.Any("error", err))
		return
	}
}

// ensureSubscriptionLocked ensures that a live RTA subscription exists on the
// Client. If one already exists it returns immediately. Otherwise it fetches a
// new subscription, coalescing concurrent callers behind a single in-flight
// fetch via subscribeDone. The caller must hold subscriptionMu on entry; the
// lock is released before returning.
func (c *Client) ensureSubscriptionLocked(ctx context.Context, seq uint64) error {
	for {
		if !c.subscriptionActive(seq) {
			c.subscriptionMu.Unlock()
			return context.Canceled
		}
		if c.subscription != nil {
			c.subscriptionMu.Unlock()
			return nil
		}
		if c.subscribeDone != nil {
			done := c.subscribeDone
			c.subscriptionMu.Unlock()
			select {
			case <-done:
				if err := ctx.Err(); err != nil {
					return err
				}
				c.subscriptionMu.Lock()
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		done := make(chan struct{})
		c.subscribeDone = done
		c.subscriptionMu.Unlock()

		subscription, err := c.fetchSubscription(ctx)

		c.subscriptionMu.Lock()
		if c.subscribeDone == done {
			c.subscribeDone = nil
			close(done)
		}
		discard := func() {
			c.subscriptionMu.Unlock()
			c.cleanupSubscription(subscription)
		}
		if err != nil {
			discard()
			return err
		}
		if !c.subscriptionActive(seq) {
			discard()
			return context.Canceled
		}
		if c.subscription != nil {
			discard()
			return nil
		}
		c.subscription = subscription
		c.subscription.Handle(&subscriptionHandler{
			Client:       c,
			subscription: subscription,
		})
		c.subscriptionMu.Unlock()
		return nil
	}
}

// fetchSubscription performs the RTA subscribe call for the caller's friends
// resource URI.
func (c *Client) fetchSubscription(ctx context.Context) (*rta.Subscription, error) {
	if c.sub == nil {
		return nil, errSubscriptionUnavailable
	}
	resourceURI := socialEndpoint.JoinPath(
		"users",
		"xuid("+c.userInfo.XUID+")",
		"friends",
	).String()
	subscription, err := c.sub.Subscribe(ctx, resourceURI)
	if err != nil {
		return nil, err
	}
	return subscription, nil
}

// ensureSubscription acquires subscriptionMu and delegates to
// ensureSubscriptionLocked.
func (c *Client) ensureSubscription(ctx context.Context, seq uint64) error {
	c.subscriptionMu.Lock()
	return c.ensureSubscriptionLocked(ctx, seq)
}

// subscriptionActive reports whether seq matches the current subscription
// sequence and the Client is not closing.
func (c *Client) subscriptionActive(seq uint64) bool {
	return c.subscriptionSeq.Load() == seq && !c.closing.Load()
}

// cleanupSubscription unsubscribes a discarded or failed RTA subscription
// with a 15 second timeout.
func (c *Client) cleanupSubscription(subscription *rta.Subscription) {
	if subscription == nil || c.unsub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	if err := c.unsub.Unsubscribe(ctx, subscription); err != nil {
		c.log.Error("error cleaning up discarded social subscription", slog.Any("error", err))
	}
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
}

// NopSubscriptionHandler is a no-op implementation of [SubscriptionHandler].
type NopSubscriptionHandler struct{}

func (NopSubscriptionHandler) HandleSocialNotification(string, []string)  {}
func (NopSubscriptionHandler) HandleIncomingFriendRequestCountChange(int) {}

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
