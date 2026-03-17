package social

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/df-mc/go-xsapi/internal"
)

// Search returns users whose gamertag or display name matches the given query
// string. Each returned [User] is only populated with 'detail' and 'preferredColor'
// decorations as passing other decorations causes an error.
func (c *Client) Search(ctx context.Context, query string, opts ...internal.RequestOption) ([]User, error) {
	requestURL := peopleHubEndpoint.JoinPath(
		"users/me/people/search/decoration/detail,preferredColor",
	)
	requestURL.RawQuery = url.Values{
		"q": []string{query},
		// Supported values are: "q" and "maxItems"
	}.Encode()

	req, err := internal.NewRequest(ctx, http.MethodGet, requestURL.String(), nil, append(
		opts,
		peopleHubContractVersion,
		internal.RequestHeader("Accept", "application/json"),
		internal.DefaultLanguage,
	))
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var respBody batchResponse
		if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
			return nil, fmt.Errorf("decode response body: %w", err)
		}
		return respBody.Users, nil
	default:
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}

// UserByXUID returns the [User] identified by the given XUID. An error is
// returned if no user with that XUID is found.
func (c *Client) UserByXUID(ctx context.Context, xuid string, opts ...internal.RequestOption) (u User, err error) {
	users, err := c.users(ctx, "me", "xuids("+xuid+")", nil, opts)
	if err != nil {
		return u, err
	}
	if len(users) == 0 {
		return u, errors.New("xsapi/social: no users found")
	}
	return users[0], nil
}

// UsersByXUIDs returns the [User] profiles for all given XUIDs in a single
// batch request. The request is sent as a POST to the batch endpoint.
func (c *Client) UsersByXUIDs(ctx context.Context, xuids []string, opts ...internal.RequestOption) ([]User, error) {
	return c.users(ctx, "me", "batch", batchRequest{
		XUIDs: xuids,
	}, opts)
}

// Friends returns the caller's friend list. Pending requests that have not
// yet been accepted are not included. Use [Client.IncomingFriendRequests] to
// retrieve requests sent to the caller, or [Client.OutgoingFriendRequests] to
// retrieve requests sent by the caller.
//
// Use [Client.FriendsOf] to retrieve the friend list of another user.
func (c *Client) Friends(ctx context.Context, opts ...internal.RequestOption) ([]User, error) {
	return c.users(ctx, "me", "friends", nil, opts)
}

// FriendsOf returns the friend list of the user identified by the given XUID.
// This can be used to retrieve the friend list of any user, not just the caller.
// See [Client.Friends] for details on how Xbox Live friend relationships work.
func (c *Client) FriendsOf(ctx context.Context, xuid string, opts ...internal.RequestOption) ([]User, error) {
	return c.users(ctx, "xuid("+xuid+")", "friends", nil, opts)
}

// IncomingFriendRequests returns the list of users who have sent the caller a
// pending friend request.
func (c *Client) IncomingFriendRequests(ctx context.Context, opts ...internal.RequestOption) ([]User, error) {
	return c.users(ctx, "me", "friendRequests(received)", nil, opts)
}

// OutgoingFriendRequests returns the list of users to whom the caller has sent
// a pending friend request.
func (c *Client) OutgoingFriendRequests(ctx context.Context, opts ...internal.RequestOption) ([]User, error) {
	return c.users(ctx, "me", "friendRequests(sent)", nil, opts)
}

// Recommendations returns the list of users recommended to the caller by Xbox
// Live. These correspond to the "Suggested Friends" section in the social
// widget and are primarily composed of friends of the caller's existing friends.
func (c *Client) Recommendations(ctx context.Context, opts ...internal.RequestOption) ([]User, error) {
	return c.users(ctx, "me", "recommendations", nil, opts)
}

// users is the shared implementation for querying multiple users via the
// PeopleHub API. perspective corresponds to the "owner" field in the Xbox Live
// API, and selector corresponds to the "people group". If postBody is non-nil,
// the request is sent as a POST with postBody JSON-encoded in the request body.
// Otherwise, a GET request is made.
func (c *Client) users(ctx context.Context, perspective, selector string, postBody any, opts []internal.RequestOption) ([]User, error) {
	var (
		requestURL = peopleHubEndpoint.JoinPath(
			"users",
			perspective,
			"people",
			selector,
			"decoration",
			decorations,
		).String()

		reqBody io.Reader
		method  string
	)
	if postBody != nil {
		buf := &bytes.Buffer{}
		defer buf.Reset()
		if err := json.NewEncoder(buf).Encode(postBody); err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		method, reqBody = http.MethodPost, buf
	} else {
		method = http.MethodGet
	}

	req, err := internal.NewRequest(ctx, method, requestURL, reqBody, append(opts,
		peopleHubContractVersion,
		internal.RequestHeader("Accept", "application/json"),
		internal.DefaultLanguage,
	))
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var respBody batchResponse
		if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
			return nil, fmt.Errorf("decode response body: %w", err)
		}
		return respBody.Users, nil
	default:
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}

type (
	// batchRequest is the wire representation of a POST request body used to
	// retrieve multiple users by XUID in a single call to the batch endpoint.
	batchRequest struct {
		// XUIDs lists the XUIDs of users to be retrieved by the batch request.
		XUIDs []string `json:"xuids"`
	}

	// batchResponse is the response body returned by the PeopleHub API,
	// containing the list of users matching the query.
	batchResponse struct {
		// Users lists the users corresponding to the query.
		Users []User `json:"people"`
	}
)

var (
	// peopleHubEndpoint is the base URL for the Xbox Live PeopleHub API.
	// PeopleHub is primarily used to query and retrieve user profiles on Xbox Live.
	//
	// Requests to this endpoint must include the 'X-Xbl-Contract-Version' header
	// set to '7'. Use the peopleHubContractVersion request option for this purpose.
	peopleHubEndpoint = &url.URL{
		Scheme: "https",
		Host:   "peoplehub.xboxlive.com",
	}
	// peopleHubContractVersion is an [internal.RequestOption] that sets an 'X-Xbl-Contract-Version'
	// header to '7' for requests made to the peopleHubEndpoint.
	peopleHubContractVersion = internal.ContractVersion("7")

	// decorations includes a full set of decorations included in the user profiles
	// returned by the methods implemented on Client.
	decorations = strings.Join([]string{
		"bio",
		"detail",
		"multiplayerSummary",
		"preferredColor",
		"presenceDetail",
	}, ",")
)

// User represents a single user profile in Xbox Live.
type User struct {
	// XUID is the unique identifier assigned to this user across Xbox Live.
	// Unlike title-scoped IDs, the XUID is network-wide and suitable for use
	// as a stable identifier for the player in most games. It is represented
	// as a decimal integer encoded in a string form.
	XUID string `json:"xuid,omitempty"`

	// Friend reports whether this user is a friend of the caller.
	Friend bool `json:"isFriend,omitempty"`
	// FriendedAt is the time at which this user and the caller became friends.
	// It is the zero value of [time.Time] when no friendship has been established.
	FriendedAt time.Time `json:"friendedDateTimeUtc,omitempty"`
	// FriendRequestReceived reports whether this user has sent a pending friend
	// request to the caller. The caller may accept it by calling [Client.AddFriend]
	// to establish a friendship.
	FriendRequestReceived bool `json:"isFriendRequestReceived,omitempty"`
	// FriendRequestSent reports whether the caller has sent a pending
	// request to this user.
	FriendRequestSent bool `json:"isFriendRequestSent,omitempty"`

	// Favorite reports whether the caller has added this user to their favorites.
	Favorite bool `json:"favorite,omitempty"`
	// Following reports whether the caller is following this user.
	Following bool `json:"isFollowingCaller,omitempty"`
	// Followed reports whether this user is following the caller.
	Followed bool `json:"isFollowedByCaller,omitempty"`

	// IdentityShared is an unknown value. It was originally believed to indicate whether
	// the user's RealName is included in their profile, but its exact meaning is currently unclear.
	IdentityShared bool `json:"isIdentityShared,omitempty"` // TODO: Find out what it actually means

	// DisplayName is the display name of the user, which corresponds to their GamerTag.
	DisplayName string `json:"displayName,omitempty"`
	// RealName is the full name associated with the user's Microsoft Account.
	// Its visibility is subject to the user's privacy settings.
	RealName string `json:"realName,omitempty"`

	// DisplayPictureRawURL is a link to the full-size display picture of the user.
	// It is recommended to use [ResizeDisplayPictureURL] to specify query parameters
	// (w, h, format) for resizing the image, since the image pointed to by DisplayPictureRawURL
	// is full-size (typically 1000x1000 or larger) and may not be suitable for display
	// to the user.
	DisplayPictureRawURL string `json:"displayPicRaw,omitempty"`
	// UseAvatar indicates whether the user has set an Xbox Live Avatar.
	// Avatars are humanoid characters representing the user, first introduced during the Xbox 360 era.
	UseAvatar bool `json:"useAvatar,omitempty"`

	// GamerTag is the gamertag of the user.
	// If the user is using a Modern GamerTag, this value is a combination
	// of ModernGamerTag and ModernGamerTagSuffix without the '#' separator.
	//
	// See also [github.com/df-mc/go-xsapi/xal/xsts.UserInfo.GamerTag].
	GamerTag string `json:"gamertag,omitempty"`
	// ModernGamerTag is the display portion of a modern gamertag, not including
	// the numeric disambiguation suffix.
	//
	// See also [github.com/df-mc/go-xsapi/xal/xsts.UserInfo.ModernGamerTag].
	ModernGamerTag string `json:"modernGamertag,omitempty"`
	// ModernGamerTagSuffix is the numeric disambiguation suffix appended to a
	// modern gamertag. The hash character ('#') that separates it from
	// [User.ModernGamerTag] is not included.
	//
	// See also [github.com/df-mc/go-xsapi/xal/xsts.UserInfo.ModernGamerTagSuffix].
	ModernGamerTagSuffix string `json:"modernGamertagSuffix,omitempty"`
	// UniqueModernGamerTag is the fully-qualified modern gamertag composed as
	// "[User.ModernGamerTag]#[User.ModernGamerTagSuffix]". This form uniquely
	// identifies the user within the modern gamertag namespace.
	//
	// See also [github.com/df-mc/go-xsapi/xal/xsts.UserInfo.UniqueModernGamerTag].
	UniqueModernGamerTag string `json:"uniqueModernGamertag,omitempty"`

	// PlayerReputation indicates the reputation of the user. How this value
	// is determined is unknown, but user reports may be involved.
	PlayerReputation string `json:"xboxOneRep,omitempty"` // TODO: Find out what it actually means

	// PresenceState indicates the online status of the user.
	// Typical values are "Online" or "Offline".
	PresenceState string `json:"presenceState,omitempty"`
	// PresenceText is a localized, user-facing value indicating the current presence of the
	// user.
	PresenceText string `json:"presenceText,omitempty"`
	// PresenceDetails lists the presence entries for titles the user is
	// currently or was previously playing. This field is only populated when
	// "presenceDetail" is specified as a decoration in the user query.
	PresenceDetails []UserPresenceDetail `json:"presenceDetails,omitempty"`

	// PreferredColor is the preferred profile color set by the user.
	// This field is only populated in the user profile when "preferredColor"
	// is included as a decoration in the user query.
	PreferredColor UserPreferredColor `json:"preferredColor,omitempty"`

	// Broadcasting reports whether the user is currently streaming on Mixer
	// (formerly Beam). Mixer was discontinued by Microsoft in 2020, so this
	// value is always false. A YouTube video published by Xbox in 2017 demonstrates
	// how this feature worked: https://youtu.be/jHcWy2B2Yy8
	Broadcasting bool `json:"isBroadcasting,omitempty"` // TODO: Figure out if this is also affected by other streaming providers, e.g. Twitch
	// Cloaked indicates whether the user is hiding their online status.
	// This value may only be valid for the caller's own user profile.
	Cloaked bool `json:"isCloaked,omitempty"`

	// LastSeenAt is the date when the user was last seen online.
	LastSeenAt time.Time `json:"lastSeenDateTimeUtc,omitempty"`

	// GamerScore is the gamerscore of the user.
	// This value increases as the user unlocks achievements.
	GamerScore json.Number `json:"gamerScore,omitempty"`
	// TitleHistory is a struct containing the history of titles played by the user.
	// It is almost nil, but is occasionally populated in the user profile.
	TitleHistory *UserTitleHistory `json:"titleHistory,omitempty"`

	// Detail describes the details of the user. It is only populated
	// when "detail" is included in the decorations of the query.
	Detail UserDetail `json:"detail,omitempty"`

	// LinkedAccounts lists the third-party platform accounts linked to this user.
	// Unless this user profile belongs to the caller themselves, only accounts
	// with ShowOnProfile enabled will be listed here.
	LinkedAccounts []UserLinkedAccount `json:"linkedAccounts,omitempty"`
	// PreferredPlatforms lists the names of the platforms preferred by the user
	// (e.g. PC, Android). Known values include "pc" and "console".
	PreferredPlatforms []string `json:"preferredPlatforms,omitempty"`

	// ColorTheme represents the profile theme set by the user.
	// Examples include "basic", "blackops6", "palworld", and "minecraft".
	ColorTheme string `json:"colorTheme,omitempty"`
}

// ResizeDisplayPictureURL returns a URL that points to a resized version of the original
// image located in the provided URL. The URL can be provided from [User.DisplayPictureRawURL],
// which is a URL locating to the full-size image that should be resized for most cases for better experience.
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

// ResizePictureOptions are the options for resizing an image located in the URL.
// It encapsulates a list of supported query parameters that can be present on
// Xbox Live's CDN URL to resize the corresponding image.
type ResizePictureOptions struct {
	// Format indicates the format of the image that should be returned by accessing
	// the output URL. It is present as the 'format' query parameter on the output URL.
	// Known value is currently only "png".
	Format string

	// Size indicates the rectangle size of the image to be resized on the output URL.
	// The first item is present as 'w' query parameter and the second item is present
	// as 'h' query parameter. Values that are too small seems to be not supported.
	Size [2]int
}

// MarshalJSON encodes the User to JSON, formatting timestamp fields as
// timezone-free UTC strings compatible with the Xbox Live API wire format.
func (u User) MarshalJSON() ([]byte, error) {
	type Alias User
	data := struct {
		Alias
		// FriendedDateTimeUTC indicates [User.FriendedAt] in a fixed, UTC timezone.
		FriendedDateTimeUTC string `json:"friendedDateTimeUtc,omitempty"`
		// LastSeenDateTimeUTC indicates [User.LastSeenAt] in a fixed, UTC timezone.
		LastSeenDateTimeUTC string `json:"lastSeenDateTimeUtc,omitempty"`
	}{Alias: (Alias)(u)}

	// Correct the timezone to the current system timezone from a fixed UTC timezone.
	if !u.FriendedAt.IsZero() {
		data.FriendedDateTimeUTC = u.FriendedAt.UTC().Format(bareTimeLayout)
	}
	if !u.LastSeenAt.IsZero() {
		data.LastSeenDateTimeUTC = u.LastSeenAt.UTC().Format(bareTimeLayout)
	}
	return json.Marshal(data)
}

// UnmarshalJSON decodes a User from JSON, parsing timestamp fields from the
// timezone-free UTC strings used in the Xbox Live API wire format.
func (u *User) UnmarshalJSON(b []byte) error {
	type Alias User
	data := struct {
		*Alias
		// FriendedDateTimeUTC indicates [User.FriendedAt] in a fixed, UTC timezone.
		FriendedDateTimeUTC string `json:"friendedDateTimeUtc,omitempty"`
		// LastSeenDateTimeUTC indicates [User.LastSeenAt] in a fixed, UTC timezone.
		LastSeenDateTimeUTC string `json:"lastSeenDateTimeUtc,omitempty"`
	}{Alias: (*Alias)(u)}
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	if data.FriendedDateTimeUTC != "" {
		var err error
		u.FriendedAt, err = time.ParseInLocation(bareTimeLayout, data.FriendedDateTimeUTC, time.UTC)
		if err != nil {
			return fmt.Errorf("xsapi/social: parse User.FriendedAt: %w", err)
		}
	}
	if data.LastSeenDateTimeUTC != "" {
		var err error
		u.LastSeenAt, err = time.ParseInLocation(bareTimeLayout, data.LastSeenDateTimeUTC, time.UTC)
		if err != nil {
			return fmt.Errorf("xsapi/social: parse User.LastSeenAt: %w", err)
		}
	}
	return nil
}

// bareTimeLayout is a variant of [time.RFC3339Nano] with the timezone designator
// omitted. It is used to parse timestamps with a fixed UTC timezone returned by
// Xbox Live API.
const bareTimeLayout = "2006-01-02T15:04:05.999999999"

// UserLinkedAccount represents an account on another social platform that is linked to the user.
type UserLinkedAccount struct {
	// NetworkName is the name of the social platform.
	NetworkName string `json:"networkName,omitempty"`

	// OptInStatus indicates the confirmation stage of whether the user has opted in to
	// linking their account on this social platform.
	// Known values include "ShowPrompt", "Excluded", and "OptedIn".
	OptInStatus string `json:"optInStatus"`

	// TokenStatus represents the stage of the authorization linkage with the other social platform.
	// Known values include "Unknown" and "NotRequired".
	// Other values may appear while the user is in the process of completing Xbox Live consent
	// on the other social platform.
	TokenStatus string `json:"tokenStatus"`

	// DisplayName is the display name of the linked account, such as its username on the
	// respective platform.
	DisplayName string `json:"displayName,omitempty"`

	// ShowOnProfile indicates whether the user has configured this account to be shown
	// on their profile. If ShowOnProfile is true, this account will appear in
	// [User.LinkedAccounts] when the user is queried.
	ShowOnProfile bool `json:"showOnProfile,omitempty"`

	// FamilyFriendly is an unknown value. It is true for some social platform accounts,
	// but what influences it is unclear.
	FamilyFriendly bool `json:"isFamilyFriendly,omitempty"`

	// Deeplink is a URL pointing to the linked account's profile on the other social platform.
	Deeplink string `json:"deeplink,omitempty"`
}

// UserDetail holds detailed information about a user profile.
type UserDetail struct {
	// CanBeFriended indicates whether the caller can add this user as a friend.
	CanBeFriended bool `json:"canBeFriended,omitempty"`
	// CanBeFollowed indicates whether the caller can follow this user.
	CanBeFollowed bool `json:"canBeFollowed,omitempty"`
	// Friend indicates whether the caller is already friends with this user.
	Friend bool `json:"friend,omitempty"`
	// FriendCount はこのユーザーのフレンド数です.
	FriendCount int `json:"friendCount,omitempty"`
	// FriendRequestReceived reports whether this user has sent a pending friend
	// request to the caller. When true, calling [Client.AddFriend] with this user
	// accepts the request and establishes a friendship.
	FriendRequestReceived bool `json:"isFriendRequestReceived,omitempty"`
	// FriendRequestSent reports whether the caller has already sent a friend request
	// to this user. When true, the pending request can be withdrawn by calling [Client.RemoveFriend].
	FriendRequestSent bool `json:"isFriendRequestSent,omitempty"`
	// FriendListShared indicates whether the caller shares their friend list
	// with this user.
	FriendListShared bool `json:"isFriendListShared,omitempty"`

	// Followed indicates whether the caller is being followed by this user.
	Followed bool `json:"isFollowingCaller,omitempty"`
	// Following indicates whether the caller is currently following this user.
	Following bool `json:"isFollowedByCaller,omitempty"`
	// Favourite indicates whether the caller has marked this user as a favourite.
	Favourite bool `json:"isFavorite,omitempty"`

	// AccountTier indicates the tier level of the user's account.
	// It defaults to Silver, and becomes Gold when the user has Xbox Game Pass.
	AccountTier string `json:"accountTier"`
	// HasGamePass indicates whether the user owns Xbox Game Pass.
	HasGamePass bool `json:"hasGamePass,omitempty"`

	// Bio is the self-description set by the user on their profile.
	Bio string `json:"bio,omitempty"`
	// Verified is unknown. It is normally false for most user profiles.
	Verified bool `json:"isVerified,omitempty"`
	// Location is the location set by the user on their profile.
	// It can be any string value that is quite small.
	Location string `json:"location,omitempty"`
	// Tenure indicates the number of years the user has owned Xbox Game Pass
	// Core/Ultimate, or Xbox Live Gold.
	Tenure json.Number `json:"tenure,omitempty"`
	// Watermarks is a list of badges earned by the user. This is typically a list of UUIDs.
	Watermarks []string `json:"watermarks,omitempty"`
	// Blocked indicates whether the caller has blocked this user.
	// Blocking another user prevents you from receiving that user's messages, game invites,
	// and party invites. It also prevents the user from seeing your presence and removes them
	// from your friend list, if they were on it. See the Xbox Support page for more:
	// https://support.xbox.com/en-US/help/friends-social-activity/friends-groups/block-or-mute-other-player
	Blocked bool `json:"blocked,omitempty"`
	// Mute indicates whether the caller has muted this user.
	// Mute prevents them from speaking to you in-game or in a chat session.
	// See the Xbox Support page for more:
	// https://support.xbox.com/en-US/help/friends-social-activity/friends-groups/block-or-mute-other-player
	Mute bool `json:"mute,omitempty"`
	// FollowerCount is the number of users following this user.
	FollowerCount int `json:"followerCount,omitempty"`
	// FollowingCount is the number of users that this user is following.
	FollowingCount int `json:"followingCount,omitempty"`
}

// UserPresenceDetail holds the details of the presence for a title that the user is
// currently or was previously playing or running. If the user is playing multiple titles
// simultaneously, [User.PresenceDetails] may contain multiple UserPresenceDetail entries.
type UserPresenceDetail struct {
	// Broadcasting indicates whether the user is streaming this title on Mixer (formerly Beam).
	// Since Mixer has been discontinued, this value is always false. A video published by Xbox
	// in 2017 demonstrates how this feature worked: https://youtu.be/jHcWy2B2Yy8
	Broadcasting bool `json:"IsBroadcasting,omitempty"` // TODO: Figure out if this is also affected by other streaming providers, e.g. Twitch
	// Device indicates the device on which this title is being played.
	// Known values include "Web", "Scarlett" (Xbox Series S/X), and "WindowsOneCore" (Windows).
	Device string `json:",omitempty"`
	// DeviceSubType is believed to indicate a subtype of the device.
	// Its exact meaning is unclear since it is nil for most presence entries.
	DeviceSubType string `json:",omitempty"`
	// GameplayType is an unknown value. It is nil in most cases.
	GameplayType string `json:",omitempty"`
	// PresenceText is a user-facing value that containing localized description of the user's
	// presence for this title.
	// When [UserPresenceDetail.State] is 'LastSeen', this value becomes a localized description
	// of the last time the user played this title, including the title name, for example:
	//   "Last seen 59d ago: PUBG BATTLEGROUNDS"
	// The language of this text is determined by the 'Accept-Language' header set by the caller.
	// Use [github.com/df-mc/go-xsapi.AcceptLanguage] in the user query to receive this text
	// in the desired language.
	PresenceText string `json:",omitempty"`
	// State indicates the state of the presence.
	// Known values include "LastSeen" and "Active".
	State string `json:",omitempty"`
	// TitleID is the identifier of the title associated with this presence, encoded
	// as a [json.Number]. The underlying type is commonly a decimal int64, while most
	// Xbox Live endpoints accepts them in string form.
	TitleID json.Number `json:"TitleId,omitempty"`
	// TitleType is an unknown value. It is nil in most cases.
	TitleType string `json:",omitempty"`
	// Primary indicates whether this presence should be displayed as the user's current status.
	Primary bool `json:"IsPrimary,omitempty"`
	// Game indicates whether this presence or its associated title is a game.
	Game bool `json:"IsGame,omitempty"`
	// RichPresenceText is a localized value providing a more detailed description of
	// what the user is doing in the title. For example, in Minecraft it may display
	// the current game mode being played.
	// The language of this text is determined by the 'Accept-Language' header set by the caller.
	RichPresenceText string `json:",omitempty"`
}

// UserTitleHistory holds the history of the last title played by the user.
// It does not include a specific Title ID or any title associations, and
// its exact purpose is unclear.
type UserTitleHistory struct {
	// LastTimePlayed is the date when this title was last played.
	LastTimePlayed time.Time `json:"lastTimePlayed"`
	// LastTimePlayedText is a user-facing value containing the date when this title
	// was last played.
	// The language of this text is determined by the 'Accept-Language' header set by the caller.
	// Use [github.com/df-mc/go-xsapi.AcceptLanguage] in the user query to receive this text
	// in the desired language.
	LastTimePlayedText string `json:"lastTimePlayedText"`
}

// UserPreferredColor represents the profile color set by the user.
type UserPreferredColor struct {
	// ColorURI is the URL to a JSON file that describes this color.
	// For example: https://dlassets-ssl.xboxlive.com/public/content/ppl/colors/00011.json
	ColorURI string `json:"colorUri"`
	// PrimaryColor is the primary color set by the user, expressed as a hex string
	// without the leading '#'. For example, '677488'.
	PrimaryColor string `json:"primaryColor,omitempty"`
	// SecondaryColor is the secondary color set by the user, expressed as a hex string
	// without the leading '#'. For example, '222B38'.
	SecondaryColor string `json:"secondaryColor,omitempty"`
	// TertiaryColor is the tertiary color set by the user, expressed as a hex string
	// without the leading '#'. For example, '3e4b61'.
	TertiaryColor string `json:"tertiaryColor,omitempty"`
}
