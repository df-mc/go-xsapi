package social

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/df-mc/go-xsapi/internal"
	"golang.org/x/text/language"
)

func (c *Client) Search(ctx context.Context, query string, opts ...internal.RequestOption) ([]User, error) {
	var (
		requestURL = peopleHubEndpoint.JoinPath(
			"users/me/people/search/decoration/detail,preferredColor",
		)
		resp batchResponse
	)
	q := requestURL.Query()
	q.Set("q", query)
	requestURL.RawQuery = q.Encode()
	if err := c.do(ctx, http.MethodGet, requestURL.String(), nil, &resp, append(
		opts,
		peopleHubContractVersion,
		defaultLanguage,
	)); err != nil {
		return nil, err
	}
	return resp.Users, nil
}

func (c *Client) UserByXUID(ctx context.Context, xuid string, opts ...internal.RequestOption) (u User, err error) {
	users, err := c.users(ctx, "me", "xuids("+xuid+")", nil, opts)
	if err != nil {
		return u, err
	}
	if n := len(users); n != 1 {
		return u, fmt.Errorf("xsapi/social: UserByXUID(%s): %d users returned", xuid, n)
	}
	return users[0], nil
}

func (c *Client) UsersByXUIDs(ctx context.Context, xuids []string, opts ...internal.RequestOption) ([]User, error) {
	return c.users(ctx, "me", "batch", batchRequest{
		XUIDs: xuids,
	}, opts)
}

func (c *Client) Friends(ctx context.Context, opts ...internal.RequestOption) ([]User, error) {
	return c.users(ctx, "me", "friends", nil, opts)
}

func (c *Client) Recommendations(ctx context.Context, opts ...internal.RequestOption) ([]User, error) {
	return c.users(ctx, "me", "recommendations", nil, opts)
}

func (c *Client) users(ctx context.Context, perspective, selector string, reqBody any, opts []internal.RequestOption) ([]User, error) {
	var (
		requestURL = peopleHubEndpoint.JoinPath(
			"users",
			perspective,
			"people",
			selector,
			"decoration",
			decorations,
		).String()

		method string
		resp   batchResponse
	)
	if reqBody != nil {
		method = http.MethodPost
	} else {
		method = http.MethodGet
	}
	return resp.Users, c.do(ctx, method, requestURL, reqBody, &resp, append(
		opts,
		peopleHubContractVersion,
		defaultLanguage,
	))
}

type (
	batchRequest struct {
		XUIDs []string `json:"xuids"`
	}
	batchResponse struct {
		Users []User `json:"people"`
	}
)

var (
	peopleHubEndpoint = &url.URL{
		Scheme: "https",
		Host:   "peoplehub.xboxlive.com",
	}
	peopleHubContractVersion = internal.ContractVersion("7")

	decorations = strings.Join([]string{
		"bio",
		"detail",
		"multiplayerSummary",
		"preferredColor",
		"presenceDetail",
	}, ",")

	defaultLanguage = internal.AcceptLanguage{
		language.AmericanEnglish,
		language.English,
	}
)

type User struct {
	XUID string `json:"xuid,omitempty"`

	Friend                bool      `json:"isFriend,omitempty"`
	FriendedAt            time.Time `json:"friendedDateTimeUtc,omitempty"`
	FriendRequestReceived bool      `json:"isFriendRequestReceived,omitempty"`
	FriendRequestSent     bool      `json:"isFriendRequestSent,omitempty"`

	Favorite  bool `json:"favorite,omitempty"`
	Followed  bool `json:"isFollowingCaller,omitempty"`
	Following bool `json:"isFollowedByCaller,omitempty"`

	IdentityShared bool `json:"isIdentityShared,omitempty"`

	DisplayName          string `json:"displayName,omitempty"`
	RealName             string `json:"realName,omitempty"`
	DisplayPictureRawURL string `json:"displayPicRaw,omitempty"`
	UseAvatar            bool   `json:"useAvatar,omitempty"`
	GamerTag             string `json:"gamertag,omitempty"`
	ModernGamerTag       string `json:"modernGamertag,omitempty"`
	ModernGamerTagSuffix string `json:"modernGamertagSuffix,omitempty"`
	UniqueModernGamerTag string `json:"uniqueModernGamertag,omitempty"`
	PlayerReputation     string `json:"xboxOneRep,omitempty"`

	PresenceState   string               `json:"presenceState,omitempty"`
	PresenceText    string               `json:"presenceText,omitempty"`
	PresenceDetails []UserPresenceDetail `json:"presenceDetails,omitempty"`

	PreferredColor UserPreferredColor `json:"preferredColor,omitempty"`

	Broadcasting bool `json:"isBroadcasting,omitempty"` // Mixer related?
	Cloaked      bool `json:"isCloaked,omitempty"`

	LastSeenAt time.Time `json:"lastSeenDateTimeUtc,omitempty"`

	GamerScore   json.Number       `json:"gamerScore,omitempty"`
	TitleHistory *UserTitleHistory `json:"titleHistory,omitempty"`
	Detail       UserDetail        `json:"detail,omitempty"`

	LinkedAccounts     []UserLinkedAccount `json:"linkedAccounts,omitempty"`
	PreferredPlatforms []string            `json:"preferredPlatforms,omitempty"`
}

func ResizeDisplayPictureURL(u string, options ResizePictureOptions) string {
	pictureURL, err := url.Parse(u)
	if err != nil {
		return u
	}
	q := pictureURL.Query()
	if options.Format != "" {
		q.Set("format", options.Format)
	}
	q.Set("w", strconv.Itoa(options.Size[0]))
	q.Set("h", strconv.Itoa(options.Size[1]))
	pictureURL.RawQuery = q.Encode()
	return pictureURL.String()
}

type ResizePictureOptions struct {
	Format string // ?format=png
	Size   [2]int // ?w=208&h=208
}

func (u User) MarshalJSON() ([]byte, error) {
	type Alias User
	data := struct {
		Alias
		FriendedDateTimeUTC string `json:"friendedDateTimeUtc,omitempty"`
		LastSeenDateTimeUTC string `json:"lastSeenDateTimeUtc,omitempty"`
	}{Alias: (Alias)(u)}
	if !u.FriendedAt.IsZero() {
		data.FriendedDateTimeUTC = u.FriendedAt.UTC().Format(utcLayout)
	}
	if !u.LastSeenAt.IsZero() {
		data.LastSeenDateTimeUTC = u.LastSeenAt.UTC().Format(utcLayout)
	}
	return json.Marshal(data)
}

func (u *User) UnmarshalJSON(b []byte) error {
	type Alias User
	data := struct {
		*Alias
		FriendedDateTimeUTC string `json:"friendedDateTimeUtc,omitempty"`
		LastSeenDateTimeUTC string `json:"lastSeenDateTimeUtc,omitempty"`
	}{Alias: (*Alias)(u)}
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	if data.FriendedDateTimeUTC != "" {
		var err error
		u.FriendedAt, err = time.ParseInLocation(utcLayout, data.FriendedDateTimeUTC, time.UTC)
		if err != nil {
			return fmt.Errorf("xsapi/social: parse User.FriendedAt: %w", err)
		}
	}
	if data.LastSeenDateTimeUTC != "" {
		var err error
		u.LastSeenAt, err = time.ParseInLocation(utcLayout, data.LastSeenDateTimeUTC, time.UTC)
		if err != nil {
			return fmt.Errorf("xsapi/social: parse User.LastSeenAt: %w", err)
		}
	}
	return nil
}

// utcLayout is time.RFC3339Nano without a timezone, used for parsing UTC times present in user profiles.
const utcLayout = "2006-01-02T15:04:05.999999999"

type UserLinkedAccount struct {
	NetworkName    string `json:"networkName,omitempty"`
	DisplayName    string `json:"displayName,omitempty"`
	ShowOnProfile  bool   `json:"showOnProfile,omitempty"`
	FamilyFriendly bool   `json:"isFamilyFriendly,omitempty"`
	Deeplink       string `json:"deeplink,omitempty"`
}

type UserDetail struct {
	CanBeFriended         bool `json:"canBeFriended,omitempty"`
	CanBeFollowed         bool `json:"canBeFollowed,omitempty"`
	Friend                bool `json:"friend,omitempty"`
	FriendCount           int  `json:"friendCount,omitempty"`
	FriendRequestReceived bool `json:"isFriendRequestReceived,omitempty"`
	FriendRequestSent     bool `json:"isFriendRequestSent,omitempty"`
	FriendListShared      bool `json:"isFriendListShared,omitempty"`

	Followed  bool `json:"isFollowingCaller,omitempty"`
	Following bool `json:"isFollowedByCaller,omitempty"`
	Favourite bool `json:"isFavorite,omitempty"`

	AccountTier string `json:"accountTier"` // Silver, Gold and Bronze? I think it is now called GamePass Core

	Bio            string      `json:"bio,omitempty"`
	Verified       bool        `json:"isVerified,omitempty"`
	Location       string      `json:"location,omitempty"`
	Tenure         json.Number `json:"tenure,omitempty"`     // How long Gold has active for.
	Watermarks     []string    `json:"watermarks,omitempty"` // TODO: Figure out what it actually means.
	Blocked        bool        `json:"blocked,omitempty"`
	Mute           bool        `json:"mute,omitempty"`
	FollowerCount  int         `json:"followerCount,omitempty"`
	FollowingCount int         `json:"followingCount,omitempty"`
	HasGamePass    bool        `json:"hasGamePass,omitempty"`
}

type UserPresenceDetail struct {
	Broadcasting     bool        `json:"IsBroadcasting,omitempty"`
	Device           string      `json:",omitempty"`
	DeviceSubType    string      `json:",omitempty"`
	GameplayType     string      `json:",omitempty"`
	PresenceText     string      `json:",omitempty"`
	State            string      `json:",omitempty"`
	TitleID          json.Number `json:"TitleId,omitempty"`
	TitleType        string      `json:",omitempty"`
	Primary          bool        `json:"IsPrimary,omitempty"`
	Game             bool        `json:"IsGame,omitempty"`
	RichPresenceText string      `json:",omitempty"`
}

type UserTitleHistory struct {
	LastTimePlayed     time.Time `json:"lastTimePlayed"`
	LastTimePlayedText time.Time `json:"lastTimePlayedText"`
}

type UserPreferredColor struct {
	PrimaryColor   string `json:"primaryColor,omitempty"`
	SecondaryColor string `json:"secondaryColor,omitempty"`
	TertiaryColor  string `json:"tertiaryColor,omitempty"`
}
