package xsts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yomoggies/xsapi-go/xal/internal"
	"github.com/yomoggies/xsapi-go/xal/xasu"
)

// Token represents an XSTS (Xbox Secure Token Service) token issued for a
// specific relying party.
//
// XSTS tokens are used to authenticate Xbox users and devices and titles when
// accessing Xbox Live services. Each token is scoped to a relying party, which
// determines which services the token is valid for and which claims are included.
//
// Common relying parties include:
//   - "http://xboxlive.com" (Xbox Live user services)
//   - Title- or service-specific relying parties
type Token internal.Token[DisplayClaims]

// Valid reports whether the token is currently usable.
func (t *Token) Valid() bool {
	return t != nil && t.Token != "" && !time.Now().After(t.NotAfter) &&
		len(t.DisplayClaims.UserInfo) > 0
}

// UserInfo returns the primary user claimed by the token.
//
// XSTS tokens may contain claims for multiple users, but in practice the
// first entry represents the authenticated user associated with the token.
// Callers should ensure the token is valid before calling this method.
func (t *Token) UserInfo() UserInfo {
	return t.DisplayClaims.UserInfo[0]
}

// String returns the authorization header value derived from the token.
//
// The returned string has the following format:
//
//	XBL3.0 x=[Token.UserInfo.UserHash];[Token.UserInfo.Token]
//
// This value is used as the HTTP Authorization header when calling Xbox Live
// services and certain title-specific endpoints. Some third-party services,
// such as PlayFab, also accept this value as a JSON field for linking an Xbox
// account to another identity provider.
func (t *Token) String() string {
	return "XBL3.0 x=" + t.UserInfo().UserHash + ";" + t.Token
}

// SetAuthHeader sets the HTTP Authorization header on req using the value
// returned by Token.String.
//
// This is a convenience method for authenticating requests to Xbox Live
// services using an XSTS token.
func (t *Token) SetAuthHeader(req *http.Request) {
	req.Header.Set("Authorization", t.String())
}

// DisplayClaims contains the claims issued by XSTS and embedded within the token.
//
// Display claims provide identity-related information about the authenticated
// user(s).
type DisplayClaims struct {
	// UserInfo lists the users claimed by the token.
	//
	// For most XSTS tokens, only a single UserInfo is claimed by the token.
	UserInfo []UserInfo `json:"xui"`
}

// UserInfo contains identity and profile information about an authenticated
// Xbox user derived from an XSTS token.
//
// Some fields are only populated when the token relies on the
// "http://xboxlive.com" relying party. Other relying parties may provide a
// reduced set of claims.
type UserInfo struct {
	xasu.UserInfo

	// GamerTag is the user's display name.
	//
	// This is the primary name shown in Xbox experiences and most games.
	// For users with modern gamertags, this value is the concatenation of
	// ModernGamerTag and ModernGamerTagSuffix without a separator.
	//
	// This field is only present in tokens that rely on the
	// party "http://xboxlive.com".
	GamerTag string `json:"gtg,omitempty"`

	// XUID is the user's unique numeric Xbox user identifier.
	//
	// XUIDs are stable across services and are commonly used for identifying
	// users in Xbox Live APIs.
	//
	// This field is only present in tokens that rely on the
	// party "http://xboxlive.com".
	XUID string `json:"xid,omitempty"`

	// ModernGamerTag is the base portion of a modern gamertag, excluding
	// the numeric suffix.
	//
	// This field is only present for users who have modern gamertags and
	// only in tokens that rely on the party "http://xboxlive.com".
	ModernGamerTag string `json:"mgt,omitempty"`

	// ModernGamerTagSuffix is the numeric suffix of a modern gamertag,
	// without the hash character.
	//
	// This field is only present for users who have modern gamertags and
	// only in tokens that rely on the party "http://xboxlive.com".
	ModernGamerTagSuffix string `json:"mgs,omitempty"`

	// UniqueModernGamerTag is the fully qualified modern gamertag in the
	// format "[UserInfo.ModernGamerTag]#[UserInfo.ModernGamerTagSuffix]".
	//
	// This value uniquely identifies the user among modern gamertags.
	// It is only present in tokens that rely on the
	// party "http://xboxlive.com".
	UniqueModernGamerTag string `json:"umg,omitempty"`

	// AgeGroup indicates the age classification of the user account.
	//
	// Typical values include "Adult", "Teen", and "Child", as defined by
	// the AgeGroup* constants in this package. Non-adult values are commonly
	// associated with family accounts managed by an adult account.
	//
	// This field is only present in tokens that rely on the
	// party "http://xboxlive.com".
	AgeGroup string `json:"agg,omitempty"`

	// Privileges lists the privileges granted to the user.
	//
	// Each entry corresponds to a numeric privilege identifier defined by
	// the Privilege* constants in this package. Privileges control access
	// to features such as multiplayer, communication, and content sharing.
	//
	// This field is only present in tokens that rely on the
	// party "http://xboxlive.com".
	Privileges Privileges `json:"prv,omitempty"`
}

// UserInfoProvider provides information about the user claimed from the Authorization
// Token (An XSTS token that relies on the party "http://xboxlive.com").
type UserInfoProvider interface {
	// UserInfo returns the UserInfo extracted from the XSTS token's
	// display claims.
	//
	// The returned value must represent valid user information derived
	// from the authorization token. Implementations are expected to
	// cache this information after the initial sign-in.
	//
	// This method does not return an error to simplify caller logic.
	UserInfo() UserInfo
}

// Privileges represents the set of privileges granted to a user.
//
// Each value is a numeric privilege identifier. Known privilege IDs
// are defined by the Privilege* constants below.
type Privileges []uint32

// MarshalJSON implements [json.Marshaler].
//
// Privileges are encoded as a single JSON string containing a
// space-delimited list of decimal privilege IDs, for example:
//
//	Privileges{185, 188, 254}
//
// encodes to:
//
//	"185 188 254"
func (p *Privileges) MarshalJSON() ([]byte, error) {
	s := make([]string, len(*p))
	for i := range len(*p) {
		s[i] = strconv.FormatUint(uint64((*p)[i]), 10)
	}
	return json.Marshal(strings.Join(s, " "))
}

// UnmarshalJSON implements [json.Unmarshaler].
//
// The JSON value must be a string containing a space-delimited list of
// decimal privilege IDs. Each ID is parsed and appended to the receiver.
func (p *Privileges) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	for char := range strings.SplitSeq(s, " ") {
		n, err := strconv.ParseUint(char, 10, 32)
		if err != nil {
			return fmt.Errorf("parse %q as uint32: %w", char, err)
		}
		*p = append(*p, uint32(n))
	}
	return nil
}

const (
	// PrivilegeCrossPlay indicates that the user is allowed to participate
	// in cross-platform gameplay with users on other platforms.
	PrivilegeCrossPlay = 185

	// PrivilegeClubs indicates that the user can create, join, and participate
	// in Xbox Live Clubs.
	PrivilegeClubs = 188

	// PrivilegeMultiplayerSessions indicates that the user can create or join
	// non-interactive multiplayer sessions.
	//
	// The exact purpose of this privilege is not fully documented.
	PrivilegeMultiplayerSessions = 189

	// PrivilegeBroadcastGameplay indicates that the user can broadcast live gameplay.
	PrivilegeBroadcastGameplay = 190

	// PrivilegeManageProfilePrivacy indicates that the user can manage profile
	// privacy settings, such as showing their real name.
	//
	// The full scope of this privilege is not fully documented.
	PrivilegeManageProfilePrivacy = 196

	// PrivilegeGameDVR indicates that the user can upload and share Game DVR clips.
	//
	// Game DVR was introduced on Xbox One and allows users to record and share gameplay.
	PrivilegeGameDVR = 198

	// PrivilegeMultiplayerParties indicates that the user can join and participate
	// in multiplayer parties.
	PrivilegeMultiplayerParties = 203

	// PrivilegeCloudManageSession indicates that the user can allocate and manage
	// Xbox Cloud Gaming (xCloud) compute sessions.
	PrivilegeCloudManageSession = 207

	// PrivilegeCloudJoinSession indicates that the user can join an Xbox Cloud Gaming
	// (xCloud) compute session.
	PrivilegeCloudJoinSession = 208

	// PrivilegeCloudSavedGames indicates that the user can save game data using
	// Xbox Cloud Gaming (xCloud) storage.
	PrivilegeCloudSavedGames = 209

	// PrivilegeSocialNetworkSharing indicates that the user can share activity on
	// their profile, including achievement unlocks and other progress.
	PrivilegeSocialNetworkSharing = 220

	// PrivilegeUserGeneratedContent indicates that the user can access
	// user-generated content (UGC) in games.
	//
	// The exact usage of this privilege is not fully documented.
	PrivilegeUserGeneratedContent = 247

	// PrivilegeCommunications indicates that the user can communicate with others
	// using text or voice chat.
	PrivilegeCommunications = 252

	// PrivilegeMultiplayer indicates that the user can join interactive
	// multiplayer game sessions.
	PrivilegeMultiplayer = 254

	// PrivilegeAddFriends indicates that the user can add friends by following
	// or connecting with other users.
	PrivilegeAddFriends = 255
)
