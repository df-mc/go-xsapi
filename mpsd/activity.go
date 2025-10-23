package mpsd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/df-mc/go-xsapi"
	"github.com/df-mc/go-xsapi/internal"
	"github.com/google/uuid"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// An ActivityFilter specifies a filter applied for searching activities on [ActivityFilter.Search]
type ActivityFilter struct {
	// Client is a [http.Client] to be used to do HTTP requests. If nil, http.DefaultClient will be copied.
	Client *http.Client

	// SocialGroup specifies a group that contains handles of activities.
	SocialGroup string
	// SocialGroupXUID references a user that does searching on specific SocialGroup.
	SocialGroupXUID string
}

func (f ActivityFilter) Search(src xsapi.TokenSource, serviceConfigID uuid.UUID) ([]ActivityHandle, error) {
	if f.Client == nil {
		f.Client = new(http.Client)
		*f.Client = *http.DefaultClient
	}
	internal.SetTransport(f.Client, src)

	owners := make(map[string]any)
	if f.SocialGroup != "" {
		if f.SocialGroupXUID == "" {
			tok, err := src.Token()
			if err != nil {
				return nil, fmt.Errorf("request token: %w", err)
			}
			f.SocialGroupXUID = tok.DisplayClaims().XUID
		}
		owners["people"] = map[string]any{
			"moniker":     f.SocialGroup,
			"monikerXuid": f.SocialGroupXUID,
		}
	}

	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(map[string]any{
		"type":   "activity",
		"scid":   serviceConfigID,
		"owners": owners,
	}); err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, searchURL.String(), buf)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("X-Xbl-Contract-Version", strconv.Itoa(contractVersion))

	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var data struct {
			Results []ActivityHandle `json:"results"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, fmt.Errorf("decode response body: %w", err)
		}
		return data.Results, nil
	default:
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}

func (conf PublishConfig) commitActivity(ctx context.Context, ref SessionReference) error {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(activityHandle{
		Type:             "activity",
		SessionReference: ref,
		Version:          1,
	}); err != nil {
		return fmt.Errorf("encode request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, handlesURL.String(), buf)
	if err != nil {
		return fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Xbl-Contract-Version", strconv.Itoa(contractVersion))

	resp, err := conf.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	default:
		return fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}

var (
	handlesURL = &url.URL{
		Scheme: "https",
		Host:   "sessiondirectory.xboxlive.com",
		Path:   "/handles",
	}

	searchURL = &url.URL{
		Scheme: "https",
		Host:   "sessiondirectory.xboxlive.com",
		Path:   "/handles/query",
		RawQuery: url.Values{
			"include": []string{"relatedInfo,customProperties"},
		}.Encode(),
	}
)

type activityHandle struct {
	Type             string           `json:"type"` // Always "activity".
	SessionReference SessionReference `json:"sessionRef,omitempty"`
	Version          int              `json:"version"` // Always 1.
	OwnerXUID        string           `json:"ownerXuid,omitempty"`
}

type ActivityHandle struct {
	activityHandle
	CreateTime       time.Time                  `json:"createTime,omitempty"`
	CustomProperties json.RawMessage            `json:"customProperties,omitempty"`
	GameTypes        json.RawMessage            `json:"gameTypes,omitempty"`
	ID               uuid.UUID                  `json:"id,omitempty"`
	InviteProtocol   string                     `json:"inviteProtocol,omitempty"`
	RelatedInfo      *ActivityHandleRelatedInfo `json:"relatedInfo,omitempty"`
	TitleID          string                     `json:"titleId,omitempty"`
}

type ActivityHandleRelatedInfo struct {
	Closed          bool      `json:"closed,omitempty"`
	InviteProtocol  string    `json:"inviteProtocol,omitempty"`
	JoinRestriction string    `json:"joinRestriction,omitempty"`
	MaxMembersCount uint32    `json:"maxMembersCount,omitempty"`
	PostedTime      time.Time `json:"postedTime,omitempty"`
	Visibility      string    `json:"visibility,omitempty"`
}

const (
	SocialGroupPeople = "people"
)
