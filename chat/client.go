package chat

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/df-mc/go-xsapi/v2/xal/nsal"
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

func (c *Client) SendUserMessage(ctx context.Context, xuid string, parts []MessageContent, opts ...internal.RequestOption) (*SendMessageResult, error) {
	return c.sendMessage(ctx, "/network/xbox/users/me/conversations/users/xuid("+xuid+")", parts, opts)
}

func (c *Client) sendMessage(ctx context.Context, path string, parts []MessageContent, opts []internal.RequestOption) (*SendMessageResult, error) {
	requestURL := endpointURL.JoinPath(path).String()
	req, err := internal.WithJSONBody(ctx, http.MethodPost, requestURL, map[string]any{
		"parts": parts,
	}, append(opts,
		internal.RequestHeader("Accept", "application/json"),
		internal.RequestHeader("Content-Type", "application/json"),
		internal.DefaultLanguage,
		contractVersion,
	))
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var result *SendMessageResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, fmt.Errorf("decode response body: %w", err)
		}
		if result == nil {
			return nil, errors.New("chat: invalid send message response")
		}
		return result, nil
	default:
		return nil, internal.UnexpectedStatusCode(resp)
	}
}

type SendMessageResult struct {
	MessageID      string `json:"messageId"`
	ConversationID string `json:"conversationId"`
}

func (c *Client) UploadImage(ctx context.Context, request *UploadImageRequest, opts ...internal.RequestOption) (*ImageContent, error) {
	attachmentID, uploadURL, err := c.uploadURL(ctx, request.FileType, opts)
	if err != nil {
		return nil, fmt.Errorf("resolve upload URL: %w", err)
	}

	// Pre-calculate the hash before reading it all.
	// I don't know if there's a good way to calculate the hash for the image.
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(request.Body); err != nil {
		return nil, fmt.Errorf("read image body: %w", err)
	}
	hash := md5.New()
	_, _ = hash.Write(buf.Bytes()) // hash.Hash states that Write should never return an error
	sum := hash.Sum(nil)
	size := buf.Len()

	req, err := internal.NewRequest(ctx, http.MethodPut, uploadURL, buf, append(opts,
		internal.ContractVersion("3"),
		internal.RequestHeader("Accept", "application/json"),
		internal.RequestHeader("x-ms-blob-type", "BlockBlob"),
		internal.RequestHeader("Content-Type", "application/octet-stream"),
		internal.RequestHeader("Cache-Control", "no-cache"),
	))
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}

	resp, err := c.client.Do(nsal.WithoutAuthHeaders(req))
	if err != nil {
		return nil, fmt.Errorf("upload image: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
		return &ImageContent{
			AttachmentID: attachmentID,
			FileType:     request.FileType,
			Size:         int64(size),
			Hash:         sum,
			Height:       request.Height,
			Width:        request.Width,
		}, nil
	default:
		return nil, internal.UnexpectedStatusCode(resp)
	}
}

type UploadImageRequest struct {
	FileType string
	Height   int
	Width    int
	Body     io.Reader
}

func (c *Client) uploadURL(ctx context.Context, fileType string, opts []internal.RequestOption) (uri string, attachmentID string, err error) {
	requestURL := endpointURL.JoinPath("/network/xbox/users/me/upload", fileType).String()
	req, err := internal.NewRequest(ctx, http.MethodGet, requestURL, nil, append(opts,
		internal.RequestHeader("Accept", "application/json"),
		internal.DefaultLanguage,
		contractVersion,
	))
	if err != nil {
		return "", "", fmt.Errorf("make request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var result struct {
			AttachmentID string `json:"attachmentId"`
			UploadURI    string `json:"uploadUri"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", "", fmt.Errorf("decode response body: %w", err)
		}
		if result.AttachmentID == "" || result.UploadURI == "" {
			return "", "", fmt.Errorf("chat: invalid image upload URL response")
		}
		return result.AttachmentID, result.UploadURI, nil
	default:
		return "", "", internal.UnexpectedStatusCode(resp)
	}
}

const (
	FileTypeJPEG = "jpg"
	FileTypePNG  = "png"
)

func (c *Client) DeleteMessage(ctx context.Context, conversation Conversation, message Message, opts ...internal.RequestOption) error {
	requestURL := endpointURL.JoinPath(
		"/network/xbox/users/me/conversations/", conversation.ID, "/messages/", message.MessageID(),
	).String()
	req, err := internal.NewRequest(ctx, http.MethodDelete, requestURL, nil, append(opts,
		internal.RequestHeader("Accept", "application/json"),
		internal.DefaultLanguage,
		contractVersion,
	))
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

func (c *Client) MarkRead(ctx context.Context, conversation Conversation, message Message, opts ...internal.RequestOption) error {
	return c.updateHorizon(ctx, []horizonUpdate{
		{
			ConversationID:   conversation.ID,
			ConversationType: conversation.Type,
			HorizonType:      "Read",
			MessageClock:     message.MessageClock(),
		},
	}, opts)
}

func (c *Client) ClearConversation(ctx context.Context, conversation ConversationResult, before Message, opts ...internal.RequestOption) error {
	return c.updateHorizon(ctx, []horizonUpdate{
		{
			ConversationID:   conversation.ID,
			ConversationType: conversation.Type,
			HorizonType:      "Delete",
			MessageClock:     before.MessageClock(),
		},
	}, opts)
}

func (c *Client) updateHorizon(ctx context.Context, updates []horizonUpdate, opts []internal.RequestOption) error {
	requestURL := endpointURL.JoinPath("/network/xbox/users/me/conversations/horizon").String()
	req, err := internal.WithJSONBody(ctx, http.MethodPost, requestURL, map[string]any{
		"conversations": updates,
	}, append(opts,
		internal.RequestHeader("Accept", "application/json"),
		internal.RequestHeader("Content-Type", "application/json"),
		internal.DefaultLanguage,
		contractVersion,
	))
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

type horizonUpdate struct {
	ConversationID   string `json:"conversationId"`
	ConversationType string `json:"conversationType"`
	HorizonType      string `json:"horizonType"`
	MessageClock     string `json:"horizon"`
}

func (c *Client) UserConversation(ctx context.Context, xuid string, filter ConversationFilter, opts ...internal.RequestOption) (*ConversationResult, error) {
	return c.conversation(ctx, "/users/xuid("+xuid+")", filter, opts)
}

func (c *Client) Conversation(ctx context.Context, typ, id string, filter ConversationFilter, opts ...internal.RequestOption) (*ConversationResult, error) {
	return c.conversation(ctx, path.Join(typ, id), filter, opts)
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
	if filter.ContinuationToken != "" {
		q.Set("continuationToken", filter.ContinuationToken)
	}
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
