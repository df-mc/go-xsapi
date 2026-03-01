package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"

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

	subscriptions   []SubscriptionHandler
	subscribed      atomic.Bool
	subscriptionsMu sync.RWMutex
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
	if !c.subscribed.Load() {
		requestURL := endpoint.JoinPath(
			"users",
			c.userInfo.XUID,
			"inbox/push",
		)
		requestURL.RawQuery = "subscriptionCategory=Microsoft.Xbox.Multiplayer,Microsoft.Xbox.People,Microsoft.Xbox.Rewards,Microsoft.Xbox.Achievements&subscriptionType=GameInvites,GamePartyInvites,GamePartyInvitesWithoutHandles,MultiplayerActivityGameInvites,PartyInvites,Followers,AcceptedFriendRequests,IncomingFriendRequests,ClaimReminder,AchievementUnlock"

		fmt.Println("ID", c.chat.ID())
		fmt.Println("Nonce", c.chat.Nonce())
		if err := c.do(ctx, http.MethodPost, requestURL, subscribeRequest{
			ConnectionID: c.chat.ID(),
			Nonce:        c.chat.Nonce(),
		}, nil, contractVersion, internal.DefaultLanguage); err != nil {
			return err
		}
		c.chat.AddHandler(&chatHandler{c})
		c.subscribed.Store(true)
	}

	c.subscriptionsMu.Lock()
	c.subscriptions = append(c.subscriptions, h)
	c.subscriptionsMu.Unlock()
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
