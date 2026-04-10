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

// New returns a new Client with the provided components.
func New(client *http.Client, userInfo xsts.UserInfo) *Client {
	return &Client{
		client:   client,
		userInfo: userInfo,
	}
}

// Client implements API client for Xbox Live Presence API.
type Client struct {
	client   *http.Client
	userInfo xsts.UserInfo
}

// Current returns the caller's current presence. Unlike [PresenceByXUID],
// this method does not require the caller to know their own XUID.
func (c *Client) Current(ctx context.Context, opts ...internal.RequestOption) (*Presence, error) {
	return c.presence(ctx, "me", opts)
}

// PresenceByXUID returns the presence of the user identified by the given XUID.
func (c *Client) PresenceByXUID(ctx context.Context, xuid string, opts ...internal.RequestOption) (*Presence, error) {
	return c.presence(ctx, "xuid("+xuid+")", opts)
}

// presence is a shared implementation for [Current] and [PresenceByXUID].
// It makes a query against the User Presence API and returns a Presence
// if one is found.
// The selector must be either "me" or "xuid(<xuid>)".
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

// Batch returns presences for all users matching the filters in the request.
func (c *Client) Batch(ctx context.Context, request BatchRequest, opts ...internal.RequestOption) (presences []*Presence, err error) {
	requestURL := endpoint.JoinPath("/users/batch").String()
	if err = internal.Do(ctx, c.client, http.MethodPost, requestURL, request, &presences, append(opts,
		internal.RequestHeader("Cache-Control", "no-cache"),
		internal.RequestHeader("Content-Type", "application/json"),
		contractVersion,
		internal.DefaultLanguage,
	)); err != nil {
		return nil, err
	}
	return presences, nil
}

// Close closes the Client with a context of 15 seconds timeout.
// It removes any presences if there was any active.
//
// In most cases, [github.com/df-mc/go-xsapi.Client.Close] should be preferred
// over calling this method directly.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()
	return c.CloseContext(ctx)
}

// CloseContext closes the Client using the given context.
// It removes the authenticated user's current title presence immediately by
// calling [Client.Remove], instead of waiting for the presence record to
// expire on the server.
//
// This method is intended for title shutdown. Callers that want to keep the
// current presence active should not call CloseContext.
//
// In most cases, [github.com/df-mc/go-xsapi.Client.CloseContext] should be preferred
// over calling this method directly.
func (c *Client) CloseContext(ctx context.Context) error {
	return c.Remove(ctx)
}

// BatchRequest describes the on-wire format for a batch presence query.
type BatchRequest struct {
	// XUIDs lists the XUIDs of the users to query. Up to 1100 XUIDs may be
	// specified at a time.
	XUIDs []string `json:"users,omitempty"`
	// DeviceTypes filters results to users active on the given device types.
	// If empty, all device types are included.
	DeviceTypes []string `json:"deviceTypes,omitempty"`
	// TitleIDs filters results to the given titles.
	// If empty, all titles are included.
	TitleIDs []string `json:"titles,omitempty"`
	// Depth controls the depth of presence data returned.
	// Possible values are defined in this package with Depth* prefix.
	// Defaults to [DepthTitle] if empty.
	Depth string `json:"level,omitempty"`
	// OnlineOnly, if true, excludes offline and cloaked users from the results.
	OnlineOnly bool `json:"onlineOnly,omitempty"`
}

const (
	// DepthUser is a presence depth that returns only the user node.
	DepthUser = "user"
	// DepthDevice is a presence depth that returns the user and device nodes.
	DepthDevice = "device"
	// DepthTitle is a presence depth that returns the whole tree except activity.
	// This is the default depth.
	DepthTitle = "title"
	// DepthAll is a presence depth that returns the whole tree, including rich
	// presence and media.
	DepthAll = "all"
)

// Remove removes the presence of the authenticated user's current title
// immediately, rather than waiting for it to expire on the server.
// It is safe to call this method even if the user doesn't have any active presence.
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

// Update updates the presence of the authenticated user's current title.
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

// TitleRequest describes the on-wire structure used to update a title's presence for the user.
type TitleRequest struct {
	// ID is the Title ID to associate with the presence. If zero, the
	// currently-authenticated title's ID is used.
	// It is unknown if the caller can specify a title ID other than the
	// currently-authenticated one.
	ID uint32 `json:"id,omitempty"`
	// Activity holds in-title details such as rich presence and media
	// information. Only titles that support these features should specify
	// this field.
	Activity *ActivityRequest `json:"activity,omitempty"`
	// State indicates whether the user is active in the title.
	// Possible values are [StateActive] and [StateInactive].
	// Defaults to [StateActive] if empty. This field is case-insensitive.
	State string `json:"state,omitempty"`
	// Placement describes how the title is displayed on screen.
	// Valid values are the Placement* constants defined in this package.
	// Defaults to [PlacementFull] if empty.
	Placement string `json:"placement,omitempty"`
}

const (
	// StateActive indicates that the user is active in the title.
	StateActive = "active"
	// StateInactive indicates that the user is no longer active in the title.
	StateInactive = "inactive"
)

const (
	// PlacementFull indicates that the title is using the full screen.
	PlacementFull = "full"
	// PlacementSnapped indicates that the title is snapped alongside another
	// application. Snap was a feature introduced on Xbox One that allowed
	// multiple titles to be displayed on screen simultaneously. It has since
	// been removed due to performance concerns, so this value is unlikely to
	// be seen. The following YouTube videos published by Xbox in 2013 demonstrates
	// how this feature worked:
	//   - https://youtu.be/Yxb3k9rptcM
	//   - https://youtu.be/pEoZbXB78NI?t=14
	PlacementSnapped = "snapped"
	// PlacementFill indicates that the title is occupying the non-snapped
	// portion of the screen while another application is snapped alongside it.
	PlacementFill = "fill"
	// PlacementBackground indicates that the title is running in the background.
	PlacementBackground = "background"
)

// ActivityRequest describes the on-wire structure used to set in-title
// presence details such as rich presence and media information.
type ActivityRequest struct {
	// RichPresence sets the rich presence string for the title. Only titles
	// that support rich presence should specify this field.
	RichPresence *RichPresenceRequest `json:"richPresence,omitempty"`
	// Media sets the media the user is currently playing. Only titles that
	// support media presence should specify this field.
	Media *MediaRequest `json:"media,omitempty"`
}

// RichPresenceRequest describes the rich presence to set for a title. For
// example, a Minecraft title may use this to reflect the game mode the user
// is currently playing.
type RichPresenceRequest struct {
	// ID is the friendly name of the rich presence string to use, as defined
	// in the title's service configuration. For example: "Creative",
	// "Survival", "Adventure".
	ID string `json:"id"`
	// ServiceConfigID is the SCID of the service configuration where the
	// rich presence strings are defined.
	ServiceConfigID uuid.UUID `json:"scid"`
	// Params is a list of friendly name strings used to complete the rich
	// presence string. Very few titles make use of this field, so its exact
	// behavior in practice is not well-documented.
	Params []string `json:"params,omitempty"`
}

// MediaRequest describes the on-wire structure used to set the user's
// currently-playing media.
type MediaRequest struct {
	// ID identifies the media. The format and semantics of this field
	// depends on IDType.
	ID string `json:"id"`
	// IDType indicates how ID should be interpreted.
	// Known values are "bing" and "provider".
	IDType string `json:"idType"`
}

var (
	// endpoint is the base URL for the Xbox Live Presence API.
	//
	// Requests sent to this endpoint must include the 'X-Xbl-Contract-Version'
	// header set to '3'. The contractVersion request option can be used
	// for this purpose.
	endpoint = &url.URL{
		Scheme: "https",
		Host:   "userpresence.xboxlive.com",
	}

	// contractVersion is an [internal.RequestOption] that sets the
	// 'X-Xbl-Contract-Version' header to '3' for requests made to the
	// endpoint.
	contractVersion = internal.ContractVersion("3")
)
