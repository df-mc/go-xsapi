package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/df-mc/go-xsapi/social/chat"
	"github.com/df-mc/go-xsapi/xal/xsts"
	"github.com/google/uuid"
)

func New(client *http.Client, chatConn *chat.Conn, userInfo xsts.UserInfo, log *slog.Logger) *Client {
	return &Client{
		client:   client,
		chat:     chatConn,
		userInfo: userInfo,
		log:      log,
	}
}

type Client struct {
	client   *http.Client
	chat     *chat.Conn
	userInfo xsts.UserInfo
	log      *slog.Logger

	subscriptionHandlers []SubscriptionHandler
	subscribed           bool
	subscriptionsMu      sync.RWMutex
}

func (c *Client) Inbox(ctx context.Context, categories []string, types []string) (*Inbox, error) {
	requestURL := endpoint.JoinPath(
		"users",
		c.userInfo.XUID,
		"inbox",
	)
	query := make(url.Values)
	query.Set("SubscriptionCategory", strings.Join(categories, ","))
	query.Set("SubscriptionType", strings.Join(types, ","))
	query.Set("maxItems", "75")

	var inbox *Inbox
	if err := c.do(ctx, http.MethodGet, requestURL, nil, &inbox, contractVersion); err != nil {
		return nil, err
	}
	if inbox == nil {
		return nil, errors.New("social/notification: invalid inbox response")
	}
	return inbox, nil
}

func (c *Client) do(ctx context.Context, method string, u *url.URL, reqBody, respBody any, opts ...internal.RequestOption) error {
	return internal.Do(ctx, c.client, method, u.String(), reqBody, respBody, opts)
}

func (c *Client) Subscribe(ctx context.Context, h SubscriptionHandler) error {
	type subscribeRequest struct {
		ConnectionID uuid.UUID `json:"ChatConnectionId"`
		Nonce        string    `json:"ChatNonce"`
	}
	if c.chat == nil {
		return errors.New("social/notification: chat is not enabled")
	}

	c.subscriptionsMu.Lock()
	defer c.subscriptionsMu.Unlock()
	if !c.subscribed {
		requestURL := endpoint.JoinPath(
			"users",
			c.userInfo.XUID,
			"inbox/push",
		)
		requestURL.RawQuery = "subscriptionCategory=Microsoft.Xbox.Multiplayer,Microsoft.Xbox.People,Microsoft.Xbox.Rewards,Microsoft.Xbox.Achievements&subscriptionType=GameInvites,GamePartyInvites,GamePartyInvitesWithoutHandles,MultiplayerActivityGameInvites,PartyInvites,Followers,AcceptedFriendRequests,IncomingFriendRequests,ClaimReminder,AchievementUnlock"

		fmt.Println("ConnectionID", c.chat.ConnectionID())
		fmt.Println("Nonce", c.chat.Nonce())
		if err := c.do(ctx, http.MethodPost, requestURL, subscribeRequest{
			ConnectionID: c.chat.ConnectionID(),
			Nonce:        c.chat.Nonce(),
		}, nil, contractVersion, internal.DefaultLanguage); err != nil {
			return err
		}
		c.chat.AddHandler(&chatHandler{c})
		c.subscribed = true
	}

	c.subscriptionHandlers = append(c.subscriptionHandlers, h)
	return nil
}

type SubscriptionHandler interface {
}

type chatHandler struct {
	client *Client
}

func (h *chatHandler) HandleServerChatMessage(envelope *chat.ServerEnvelope) {
	fmt.Println(string(envelope.Raw))
}

var (
	endpoint = &url.URL{
		Scheme: "https",
		Host:   "notificationinbox.xboxlive.com",
	}

	contractVersion = internal.ContractVersion("1")
)
