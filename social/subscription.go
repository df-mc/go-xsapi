package social

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
)

func (c *Client) Subscribe(ctx context.Context, h SubscriptionHandler) (err error) {
	if h == nil {
		return errors.New("xsapi/social: cannot subscribe with a nil SubscriptionHandler")
	}

	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	if c.subscription != nil {
		c.subscriptionHandlers = append(c.subscriptionHandlers, h)
		return nil
	}

	resourceURI := socialEndpoint.JoinPath(
		"users",
		"xuid("+c.api.UserInfo().XUID+")",
		"friends",
	).String()
	subscription, err := c.api.RTA().Subscribe(ctx, resourceURI)
	if err != nil {
		return err
	}
	c.subscriptionHandlers = append(c.subscriptionHandlers, h)
	subscription.Handle(&subscriptionHandler{c})
	return nil
}

type subscriptionHandler struct {
	*Client
}

func (h *subscriptionHandler) HandleEvent(custom json.RawMessage) {
	var data struct {
		Type string `json:"NotificationType"`

		// Fields for IncomingFriendRequestCountChanged
		Count *int `json:"Count"`

		// Fields for relationship notifications
		XUIDs []string `json:"Xuids"`
	}
	if err := json.Unmarshal(custom, &data); err != nil {
		h.api.Log().Error("error decoding subscription custom", slog.Any("error", err))
		return
	}
	switch data.Type {
	case "IncomingFriendRequestCountChanged":
		if data.Count == nil {
			h.api.Log().Error("friend request count is absent in subscription event data",
				slog.String("custom", string(custom)),
			)
			return
		}
		h.subscriptionMu.Lock()
		for _, handler := range h.subscriptionHandlers {
			handler.HandleFriendRequestCountChange(*data.Count)
		}
		h.subscriptionMu.Unlock()
		return
	case RelationshipChangeTypeAdded, RelationshipChangeTypeRemoved, RelationshipChangeTypeChanged:
		if len(data.XUIDs) == 0 {
			h.api.Log().Error("XUIDs are absent from subscription event data",
				slog.String("custom", string(custom)),
			)
			return
		}
		h.subscriptionMu.Lock()
		for _, handler := range h.subscriptionHandlers {
			handler.HandleRelationshipChange(RelationshipChange{
				Type:  data.Type,
				XUIDs: data.XUIDs,
			})
		}
		h.subscriptionMu.Unlock()
	default:
		h.api.Log().Warn("unexpected subscription notification type",
			slog.String("type", data.Type),
		)
	}
}

type SubscriptionHandler interface {
	HandleRelationshipChange(change RelationshipChange)
	HandleFriendRequestCountChange(count int)
}

type NopSubscriptionHandler struct{}

func (NopSubscriptionHandler) HandleRelationshipChange(RelationshipChange) {}
func (NopSubscriptionHandler) HandleFriendRequestCountChange(count int)    {}

type RelationshipChange struct {
	Type  string   `json:"NotificationType,omitempty"`
	XUIDs []string `json:"Xuids,omitempty"`
}

const (
	RelationshipChangeTypeAdded   = "Added"
	RelationshipChangeTypeRemoved = "Removed"
	RelationshipChangeTypeChanged = "Changed"
)
