package xsts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/df-mc/go-xsapi/xal/internal"
	"github.com/df-mc/go-xsapi/xal/xasu"
)

type Token internal.Token[DisplayClaims]

func (t *Token) Valid() bool {
	return t != nil && t.Token != "" && !time.Now().After(t.NotAfter) &&
		len(t.DisplayClaims.UserInfo) > 0
}

func (t *Token) UserInfo() UserInfo {
	return t.DisplayClaims.UserInfo[0]
}

func (t *Token) String() string {
	return "XBL3.0 x=" + t.UserInfo().UserHash + ";" + t.Token
}

func (t *Token) SetAuthHeader(req *http.Request) {
	req.Header.Set("Authorization", t.String())
}

type DisplayClaims struct {
	UserInfo []UserInfo `json:"xui"`
}

type UserInfo struct {
	xasu.UserInfo

	// GamerTag is the unique name for the user.
	// It is the common name used to display the name of the user in most games,
	// including Minecraft: Bedrock Edition.
	// If the user has a modern gamertag, it will be a combination of ModernGamerTag
	// and ModernGamerTagSuffix with no delimiters. e.g TODO
	GamerTag string `json:"gtg,omitempty"`

	// XUID is the unique, numerical ID for the user.
	// It is only claimed on user tokens that relies on the party "http://xboxlive.com".
	XUID string `json:"xid,omitempty"`

	// ModernGamerTag is the modern gamertag of the user without the suffix. e.g. TODO
	// It is only claimed on users with modern gamertag, and only for user
	// tokens that relies on the party "http://xboxlive.com".
	ModernGamerTag string `json:"mgt,omitempty"`

	// ModernGamerTagSuffix is the suffix of the modern gamertag without the hash character. e.g. TODO
	// It is only claimed on users with modern gamertag, and only for user
	// tokens that relies on the party "http://xboxlive.com".
	ModernGamerTagSuffix string `json:"mgs,omitempty"`

	// UniqueModernGamerTag is the full modern gamer tag of the user in the format
	// '[UniqueModernGamerTag]#[UniqueModernGamerTagSuffix]'.
	// It is only claimed on user tokens that relies on the party "http://xboxlive.com".
	UniqueModernGamerTag string `json:"umg,omitempty"`

	// AgeGroup categories which age group the user is in.
	// It is typically 'Adult' for most accounts, with exceptions
	// being family accounts associated to the parents.
	// Examples can be found in the constants defined in this package.
	// It is only claimed on user tokens that relies on the party "http://xboxlive.com".
	AgeGroup string `json:"agg,omitempty"`

	// Privileges is a list of privileges allowed on the user.
	// It is only claimed on user tokens that relies on the party "http://xboxlive.com".
	Privileges Privileges `json:"prv,omitempty"`
}

type UserInfoProvider interface {
	UserInfo() UserInfo
}

const (
	// AgeGroupAdult categorizes user as an Adult.
	AgeGroupAdult = "Adult"
	// AgeGroupTeen categories user as a Teen associated to an Adult account.
	AgeGroupTeen = "Teen"
	// AgeGroupChild categories user as a Child associated to an Adult account.
	AgeGroupChild = "Child"
)

// Privileges is a list of allowed privileges on the user.
// The value may be found in the constants below.
type Privileges []uint32

// UnmarshalJSON decodes the JSON data into a string that contains
// a space-delimited list of numeric privilege IDs.
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

func (p *Privileges) MarshalJSON() ([]byte, error) {
	s := make([]string, len(*p))
	for i := range len(*p) {
		s[i] = strconv.FormatUint(uint64((*p)[i]), 10)
	}
	return json.Marshal(strings.Join(s, " "))
}

const (
	// PrivilegeCrossPlay indicates that the user is allowed to participate in
	// people on other platforms.
	PrivilegeCrossPlay = 185
	// PrivilegeClubs indicates that the user can create, join or participate in
	// Clubs on Xbox Live.
	PrivilegeClubs = 188
	// PrivilegeMultiplayerSessions indicates that the user can create or join
	// non-interactive multiplayer sessions. It is unknown what it is used for.
	PrivilegeMultiplayerSessions = 189
	// PrivilegeBroadcastGameplay indicates that the user can broadcast live gameplay.
	PrivilegeBroadcastGameplay = 190
	// PrivilegeManageProfilePrivacy indicates that the user is allowed to change settings
	// to show real name. It is unknown what it is used for.
	PrivilegeManageProfilePrivacy = 196
	// PrivilegeGameDVR indicates that the user is allowed to upload GameDVR, a feature introduced
	// on Xbox One as an Xbox Live Gold Membership benefit, allows you to record and share your gameplay.
	PrivilegeGameDVR = 198
	// PrivilegeMultiplayerParties is a privilege that allows the user is allowed to join parties on multiplayer.
	PrivilegeMultiplayerParties = 203
	// PrivilegeCloudManageSession is a privilege that allows the user is allowed to allocate XCloud computes.
	PrivilegeCloudManageSession = 207
	// PrivilegeCloudJoinSession is a privilege that allows user to join an XCloud compute session.
	PrivilegeCloudJoinSession = 208
	// PrivilegeCloudSavedGames is a privilege that allows the user to save games on XCloud.
	PrivilegeCloudSavedGames = 209
	// PrivilegeSocialNetworkSharing indicates the user can share their progress on the profile,
	// including achievements unlocks and more.
	PrivilegeSocialNetworkSharing = 220
	// PrivilegeUserGeneratedContent indicates that the user is allowed to access user generated content (UGC)
	// in game. It is unknown what it is used for.
	PrivilegeUserGeneratedContent = 247
	// PrivilegeCommunications indicates that the user is allowed for chatting with other people using text or voice.
	PrivilegeCommunications = 252
	// PrivilegeMultiplayer indicates that the user is allowed to join multiplayer sessions.
	PrivilegeMultiplayer = 254
	// PrivilegeAddFriends indicates that the user is allowed to add friends by following users.
	PrivilegeAddFriends = 255
)
