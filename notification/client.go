package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

// New returns a Client using the given components.
func New(client *http.Client, userInfo xsts.UserInfo, log *slog.Logger) *Client {
	return &Client{
		client:   client,
		userInfo: userInfo,
		log:      log,
	}
}

// Client implements an API Client for Xbox Live Notification API.
type Client struct {
	client   *http.Client
	userInfo xsts.UserInfo
	log      *slog.Logger
}

// Inbox returns the caller's notification inbox. The filter may be used to limit
// which notifications are populated in the result.
func (c *Client) Inbox(ctx context.Context, filter InboxFilter, opts ...internal.RequestOption) ([]Notification, error) {
	if filter.MaxItems == 0 {
		filter.MaxItems = 200
	}
	if filter.MaxActions == 0 {
		filter.MaxActions = 5
	}
	if len(filter.SubscriptionCategories) == 0 {
		filter.SubscriptionCategories = defaultPool.categories()
	}
	if len(filter.SubscriptionTypes) == 0 {
		filter.SubscriptionTypes = defaultPool.types()
	}

	requestURL := endpointURL.JoinPath("/users/", c.userInfo.XUID, "/inbox")
	q := requestURL.Query()
	q.Set("maxItems", strconv.Itoa(filter.MaxItems))
	q.Set("maxActions", strconv.Itoa(filter.MaxActions))
	q.Set("subscriptionCategory", strings.Join(filter.SubscriptionCategories, ","))
	q.Set("subscriptionType", strings.Join(filter.SubscriptionTypes, ","))
	requestURL.RawQuery = q.Encode()

	req, err := internal.NewRequest(ctx, http.MethodGet, requestURL.String(), nil, append(opts, contractVersion))
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response body: %w", err)
	}
	if len(result.Items) == 0 {
		return nil, nil
	}
	notifications := make([]Notification, 0, len(result.Items))
	for _, item := range result.Items {
		n, err := Unmarshal(item)
		if err != nil {
			c.log.Error("error decoding inbox item", "error", err)
			continue
		}
		notifications = append(notifications, n)
	}
	return notifications, nil
}

// InboxFilter represents a filter used for retrieving the notification inbox for the caller.
type InboxFilter struct {
	// MaxItems specifies the maximum amount of notifications that can
	// be included in the result. When zero, it will be set to 200.
	MaxItems int
	// MaxActions specifies the maximum amount of actions that can be
	// embedded to each notification included in the result. When zero,
	// it wil be set to 5.
	MaxActions int

	// SubscriptionCategories specifies the list of subscription categories
	// that will be populated in the result. If empty, the categories
	// supported in this package are used instead.
	SubscriptionCategories []string
	// SubscriptionTypes specifies the list of subscription types that will
	// be populated in the result. If empty, all subscription types supported
	// in this package are used instead.
	SubscriptionTypes []string
}

// Dismiss dismisses the given notification. The notification will no longer
// be included in the result of subsequent callers to [Client.Inbox].
func (c *Client) Dismiss(ctx context.Context, notification Notification, opts ...internal.RequestOption) error {
	return c.Update(ctx, []Notification{notification}, UpdateTypeLastDeleted, time.Now(), opts...)
}

// MarkRead marks the given notification as read. [Notification.MarkedRead] may be set
// to true the next time this notification is retrieved. If the notification is later
// updated, MarkedRead reverts to false, so this method must be called again to mark it
// as read once more. It is unclear how this differs from [Client.MarkSeen].
func (c *Client) MarkRead(ctx context.Context, notification Notification, opts ...internal.RequestOption) error {
	return c.Update(ctx, []Notification{notification}, UpdateTypeLastRead, time.Now(), opts...)
}

// MarkSeen marks the given notification as seen. [Notification.Seen] may be set to
// true the next time this notification is retrieved. If the notification is later updated,
// Seen reverts to false, so this method must be called again to mark it as seen once more.
// It is unclear how this differs from [Client.MarkRead].
func (c *Client) MarkSeen(ctx context.Context, notification Notification, opts ...internal.RequestOption) error {
	return c.Update(ctx, []Notification{notification}, UpdateTypeLastSeen, time.Now(), opts...)
}

// Update updates the given notifications with the given update type and the timestamp.
// The type must be one of the UpdateType constants defined below. The timestamp records
// when the update occurred and is used to determine which notifications have unread updates.
func (c *Client) Update(ctx context.Context, notifications []Notification, typ string, timestamp time.Time, opts ...internal.RequestOption) error {
	items := make([]notificationKey, len(notifications))
	for i, n := range notifications {
		items[i] = notificationKey{
			SubscriptionCategory: n.SubscriptionCategory(),
			SubscriptionType:     n.SubscriptionType(),
			SubscriptionID:       n.SubscriptionID(),
		}
	}
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	requestURL := endpointURL.JoinPath("/users/", c.userInfo.XUID, "/inbox/subscriptions/batch").String()
	req, err := internal.WithJSONBody(ctx, http.MethodPost, requestURL, updateRequest{
		Items:      items,
		Timestamp:  timestamp,
		UpdateType: typ,
	}, append(opts, contractVersion))
	if err != nil {
		return fmt.Errorf("make request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	default:
		return internal.UnexpectedStatusCode(resp)
	}
}

const (
	UpdateTypeLastSeen    = "LastSeen"
	UpdateTypeLastRead    = "LastRead"
	UpdateTypeLastDeleted = "LastDeleted"
)

// updateRequest represents the JSON request payload used to update notifications.
type updateRequest struct {
	// Items lists the minimal identifying fields of each notification
	// updated by this request.
	Items []notificationKey `json:"items"`
	// Timestamp is the timestamp associated with the request.
	Timestamp time.Time
	// UpdateType indicates the type of the update request.
	// It is one of the UpdateType constants defined above.
	UpdateType string
}

var (
	// endpointURL is the base URL used to make requests to the Xbox Live Notification API.
	//
	// Requests sent to this endpoint must include the 'X-Xbl-Contract-Version'
	// header set to '1'. The contractVersion request option can be also
	// used for this purpose.
	endpointURL = &url.URL{
		Scheme: "https",
		Host:   "notificationinbox.xboxlive.com",
	}
	// contractVersion is an [internal.RequestOption] that sets the
	// 'X-Xbl-Contract-Version' header to '1' for requests made to the
	// endpointURL.
	contractVersion = internal.ContractVersion("1")
)
