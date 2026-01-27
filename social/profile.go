package social

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// TODO: Handle and map fields automatically to Profile rather than using an array of key-value struct

func (c *Client) Profiles(ctx context.Context, xuids []string, settings ...string) ([]Profile, error) {
	if len(settings) == 0 {
		settings = ProfileFields
	}
	if len(xuids) == 0 || len(settings) == 0 {
		return nil, fmt.Errorf("xsapi/social: invalid request: %d XUIDs and %d settings specified", len(xuids), len(settings))
	}

	var (
		resp       batchResponse
		requestURL = profileEndpoint.JoinPath("/users/batch/profile/settings").String()
	)
	return resp.Profiles, c.do(ctx, http.MethodPost, requestURL, batchRequest{
		UserIDs:  xuids,
		Settings: settings,
	}, &resp)
}

type batchRequest struct {
	UserIDs  []string `json:"userIds"`
	Settings []string `json:"settings"`
}

type batchResponse struct {
	Profiles []Profile `json:"profileUsers"`
}

type Profile struct {
	Settings json.RawMessage `json:"settings"`
}

var profileEndpoint = &url.URL{
	Scheme: "https",
	Host:   "profile.xboxlive.com",
}

const (
	ProfileAppDisplayName       = "AppDisplayName"
	ProfileAppDisplayPicRaw     = "AppDisplayPicRaw"
	ProfileGameDisplayName      = "GameDisplayName"
	ProfileGameDisplayPicRaw    = "GameDisplayPicRaw"
	ProfileGamerscore           = "Gamerscore"
	ProfileGamerTag             = "Gamertag"
	ProfileModernGamerTag       = "ModernGamertag"
	ProfileModernGamerTagSuffix = "ModernGamertagSuffix"
	ProfileUniqueModernGamerTag = "UniqueModernGamertag"
)

var ProfileFields = []string{
	ProfileAppDisplayName,
	ProfileAppDisplayPicRaw,
	ProfileGameDisplayName,
	ProfileGameDisplayPicRaw,
	ProfileGamerscore,
	ProfileGamerTag,
	ProfileModernGamerTag,
	ProfileModernGamerTagSuffix,
	ProfileUniqueModernGamerTag,
}
