package presence

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/df-mc/go-xsapi/xal/xsts"
	"github.com/google/uuid"
)

func New(client *http.Client, userInfo xsts.UserInfo) *Client {
	return &Client{
		client:   client,
		userInfo: userInfo,
	}
}

type Client struct {
	client   *http.Client
	userInfo xsts.UserInfo
}

func (c *Client) Current(ctx context.Context, opts ...internal.RequestOption) (*Presence, error) {
	return c.presence(ctx, "me", opts)
}

func (c *Client) PresenceByXUID(ctx context.Context, xuid string, opts ...internal.RequestOption) (*Presence, error) {
	return c.presence(ctx, "xuid("+xuid+")", opts)
}

func (c *Client) presence(ctx context.Context, selector string, opts []internal.RequestOption) (*Presence, error) {
	var (
		requestURL = endpoint.JoinPath(
			"users",
			selector,
		)
		presence *Presence
	)
	q := make(url.Values)
	q.Set("level", "all")
	requestURL.RawQuery = q.Encode()

	if err := internal.Do(ctx, c.client, http.MethodGet, requestURL.String(), nil, &presence, append(opts,
		contractVersion,
		internal.RequestHeader("Cache-Control", "no-cache"),
		internal.RequestHeader("Content-Type", "application/json"),
		internal.DefaultLanguage,
	)); err != nil {
		return nil, err
	}
	if presence == nil {
		return nil, errors.New("xsapi/presence: invalid presence response")
	}
	return presence, nil
}

func (c *Client) Batch(ctx context.Context, request BatchRequest, opts ...internal.RequestOption) (presences []*Presence, err error) {
	requestURL := endpoint.JoinPath("/users/batch").String()
	if err = internal.Do(ctx, c.client, http.MethodPost, requestURL, request, &presences, append(opts,
		internal.RequestHeader("Cache-Control", "no-cache"),
		internal.RequestHeader("Content-Type", "application/json"),
		internal.DefaultLanguage,
	)); err != nil {
		return nil, err
	}
	return presences, nil
}

func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return c.CloseContext(ctx)
}

func (c *Client) CloseContext(ctx context.Context) error {
	return c.Remove(ctx)
}

type BatchRequest struct {
	XUIDs       []string `json:"users,omitempty"`
	DeviceTypes []string `json:"deviceTypes,omitempty"`
	TitleIDs    []string `json:"titles,omitempty"`
	Level       string   `json:"level,omitempty"`
	OnlineOnly  bool     `json:"onlineOnly,omitempty"`
}

func (c *Client) Remove(ctx context.Context, opts ...internal.RequestOption) error {
	requestURL := endpoint.JoinPath(
		"users",
		"xuid("+c.userInfo.XUID+")",
		"/devices/current/titles/current",
	).String()

	// This request is a DELETE call but returns 200 OK instead of 204 No Content.
	return internal.Do(ctx, c.client, http.MethodDelete, requestURL, nil, nil, append(opts,
		contractVersion,
		internal.RequestHeader("Cache-Control", "no-cache"),
		internal.RequestHeader("Content-Type", "application/json"),
		internal.DefaultLanguage,
	))
}

func (c *Client) Update(ctx context.Context, request TitleRequest, opts ...internal.RequestOption) error {
	requestURL := endpoint.JoinPath(
		"users",
		"xuid("+c.userInfo.XUID+")",
		"/devices/current/titles/current",
	).String()
	return internal.Do(ctx, c.client, http.MethodPost, requestURL, request, nil, append(opts,
		contractVersion,
		internal.RequestHeader("Cache-Control", "no-cache"),
		internal.RequestHeader("Content-Type", "application/json"),
		internal.DefaultLanguage,
	))
}

type TitleRequest struct {
	ID        uint32           `json:"id,omitempty"`
	Activity  *ActivityRequest `json:"activity,omitempty"`
	State     string           `json:"state,omitempty"`
	Placement string           `json:"placement,omitempty"`
}

const (
	StateActive   = "active"
	StateInactive = "inactive"
)

const (
	PlacementFull       = "full"
	PlacementFill       = "fill"
	PlacementSnapped    = "snapped"
	PlacementBackground = "background"
)

type ActivityRequest struct {
	RichPresence *RichPresenceRequest `json:"richPresence,omitempty"`
	Media        *MediaRequest        `json:"media,omitempty"`
}

type RichPresenceRequest struct {
	ID              string    `json:"id"`
	ServiceConfigID uuid.UUID `json:"scid"`
	Params          []string  `json:"params,omitempty"`
}

type MediaRequest struct {
	ID     string `json:"id"`
	IDType string `json:"idType"`
}

var (
	endpoint = &url.URL{
		Scheme: "https",
		Host:   "userpresence.xboxlive.com",
	}

	contractVersion = internal.ContractVersion("3")
)
