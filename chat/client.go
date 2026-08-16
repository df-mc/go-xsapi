package chat

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

func New(client *http.Client, userInfo xsts.UserInfo, log *slog.Logger) *Client {
	return &Client{
		client:   client,
		userInfo: userInfo,
		log:      log,
	}
}

type Client struct {
	client   *http.Client
	userInfo xsts.UserInfo
	log      *slog.Logger
}

func (c *Client) Inbox(ctx context.Context, opts ...internal.RequestOption) (*Inbox, error) {
	var (
		requestURL = endpointURL.JoinPath("/network/xbox/users/me/inbox").String()
		inbox      *Inbox
	)
	if err := internal.Do(ctx, c.client, http.MethodGet, requestURL, nil, &inbox, append(opts,
		internal.RequestHeader("Accept", "application/json"),
		internal.DefaultLanguage,
		contractVersion,
	)); err != nil {
		return nil, err
	}
	if inbox == nil {
		return nil, errors.New("chat: invalid Inbox response")
	}
	return inbox, nil
}

func (c *Client) InboxFolder(ctx context.Context, name string, opts ...internal.RequestOption) (*InboxFolder, error) {
	var (
		requestURL = endpointURL.JoinPath("/network/xbox/users/me/inbox", name).String()
		folder     *InboxFolder
	)
	if err := internal.Do(ctx, c.client, http.MethodGet, requestURL, nil, &folder, append(opts,
		internal.RequestHeader("Accept", "application/json"),
		internal.DefaultLanguage,
		contractVersion,
	)); err != nil {
		return nil, err
	}
	if folder == nil {
		return nil, errors.New("chat: invalid InboxFolder response")
	}
	return folder, nil
}

func (c *Client) MarkRead(ctx context.Context, conversations []Conversation, opts ...internal.RequestOption) error {

}

func (c *Client) UserConversation(ctx context.Context, xuid string, filter ConversationFilter, opts ...internal.RequestOption) (*ConversationResult, error) {
	return c.conversation(ctx, "/users/xuid("+xuid+")", filter, opts)
}

func (c *Client) conversation(ctx context.Context, path string, filter ConversationFilter, opts []internal.RequestOption) (*ConversationResult, error) {
	if filter.MaxItems == 0 {
		filter.MaxItems = 100
	}
	var (
		requestURL = endpointURL.JoinPath(
			"/network/xbox/users/me/conversations", path,
		)
		result *ConversationResult
	)
	q := requestURL.Query()
	q.Set("maxItems", strconv.Itoa(filter.MaxItems))
	q.Set("continuationToken", filter.ContinuationToken)
	requestURL.RawQuery = q.Encode()

	if err := internal.Do(ctx, c.client, http.MethodGet, requestURL.String(), nil, &result, append(opts,
		contractVersion,
		internal.RequestHeader("Accept", "application/json"),
		internal.DefaultLanguage,
	)); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("chat: invalid UserConversation response")
	}
	return result, nil
}

type ConversationFilter struct {
	MaxItems          int
	ContinuationToken string
}

var (
	endpointURL = &url.URL{
		Scheme: "https",
		Host:   "xblmessaging.xboxlive.com",
	}
	contractVersion = internal.ContractVersion("1")
)
