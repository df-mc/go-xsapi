package social

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/df-mc/go-xsapi/v2/xal/nsal"
)

// UpdateProfile updates the profile settings of the caller. Only non-nil field values
// specified in the given ProfileSettings are applied.
func (c *Client) UpdateProfile(ctx context.Context, settings ProfileSettings, opts ...internal.RequestOption) error {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(settings); err != nil {
		return fmt.Errorf("encode request body: %w", err)
	}

	requestURL := profileEndpoint.JoinPath(
		"/users/me/profile/settings/batch",
	).String()
	req, err := internal.NewRequest(ctx, http.MethodPost, requestURL, buf, append(opts,
		socialContractVersion,
		internal.DefaultLanguage,
		internal.RequestHeader("Content-Type", "application/json"),
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
	case http.StatusCreated:
		return nil
	default:
		return internal.UnexpectedStatusCode(resp)
	}
}

// ProfileSettings describes the settings to commit to the user's profile.
// Each field can be nil if the user has not specified the setting.
type ProfileSettings struct {
	// Bio is the user's profile description.
	Bio *string
	// Location is the location displayed on the user's profile.
	// It can be any arbitrary string specified by the user.
	Location *string
	// ShowUserAsAvatar indicates whether to display the user's Xbox Live
	// Avatar when viewing their profile. This feature is no longer available
	// in the Xbox app.
	ShowUserAsAvatar *bool
	// PreferredPlatforms lists the platforms preferred by the user.
	// Known values are listed below as constants.
	PreferredPlatforms *[]string
	// WebColorTheme specifies the theme applied to the user's profile.
	// Setting it to 'gamerpicblur' displays a blurred version of the user's profile image
	// as the background. Other supported themes are defined in the manifest file
	// that can be found here:
	//   https://dlassets-ssl.xboxlive.com/public/content/ppl/profilethemes/v2/manifests/en-US.json
	WebColorTheme *string
	// Custom specifies the list of profile setting that was not described/implemented in
	// this struct. It allows caller to set unknown setting with an ID and the value.
	Custom []ProfileSetting
}

// MarshalJSON implements [json.Marshaler].
func (s *ProfileSettings) MarshalJSON() ([]byte, error) {
	var settings []ProfileSetting
	for key, value := range s.stringFields() {
		if value != nil {
			settings = append(settings, ProfileSetting{
				ID:    key,
				Value: *value,
			})
		}
	}
	if s.PreferredPlatforms != nil {
		settings = append(settings, ProfileSetting{
			ID:    ProfileSettingPreferredPlatforms,
			Value: strings.Join(*s.PreferredPlatforms, ","),
		})
	}
	if s.ShowUserAsAvatar != nil {
		var value string
		if *s.ShowUserAsAvatar {
			value = "1"
		} else {
			value = "0"
		}
		settings = append(settings, ProfileSetting{
			ID:    ProfileSettingShowUserAsAvatar,
			Value: value,
		})
	}

	settings = slices.Concat(slices.DeleteFunc(settings, func(left ProfileSetting) bool {
		return slices.ContainsFunc(s.Custom, func(right ProfileSetting) bool {
			return left.ID == right.ID
		})
	}), s.Custom)

	var data struct {
		Settings []settingsEnvelope `json:"settings"`
	}
	data.Settings = make([]settingsEnvelope, len(settings))
	for i, setting := range settings {
		data.Settings[i].ProfileSetting = setting
	}
	return json.Marshal(data)
}

// stringFields returns a map whose key is the ID of the profile setting
// and the value is a *string which may be non-nil if the caller has specified
// a value for it.
func (s *ProfileSettings) stringFields() map[string]*string {
	return map[string]*string{
		ProfileSettingBio:           s.Bio,
		ProfileSettingLocation:      s.Location,
		ProfileSettingWebColorTheme: s.WebColorTheme,
	}
}

type (
	// ProfileSetting represents a single setting in the user profile.
	ProfileSetting struct {
		// ID is the name of the profile setting.
		// It is one of the constants listed below.
		ID string `json:"id"`
		// Value is the value of the profile setting.
		Value string `json:"value"`
	}

	// settingsEnvelope is a struct that encapsulates ProfileSetting. It is used
	// for updating user profiles.
	settingsEnvelope struct {
		// ProfileSetting is the profile setting to be applied for the user.
		ProfileSetting ProfileSetting `json:"userSetting"`
	}
)

const (
	// ProfileSettingBio is the profile setting name used to specify the
	// user's bio or description. The value can be any arbitrary string.
	ProfileSettingBio = "Bio"
	// ProfileSettingLocation is the profile setting name used to specify the
	// user's location. The value can be any arbitrary string.
	ProfileSettingLocation = "Location"
	// ProfileSettingShowUserAsAvatar is the profile setting name used to
	// indicate whether the user's Xbox Live Avatar should be displayed on
	// their profile. The setting is no longer supported by the Xbox app.
	// The value is encoded as the string '1' when enabled and '0' when disabled.
	ProfileSettingShowUserAsAvatar = "ShowUserAsAvatar"
	// ProfileSettingPreferredPlatforms is the profile setting name used to specify
	// the user's preferred platforms. The value is encoded as a comma-separated
	// list of platform names listed below as constants.
	ProfileSettingPreferredPlatforms = "PreferredPlatforms"
	// ProfileSettingWebColorTheme is the profile setting name used to
	// specify the theme configured for the user's profile.
	ProfileSettingWebColorTheme = "WebColorTheme"
)

const (
	// PreferredPlatformPC indicates that the user's preferred platform is PC (Windows).
	PreferredPlatformPC = "pc"
	// PreferredPlatformMobile indicates that the user's preferred platform is Mobile (Android/iOS).
	PreferredPlatformMobile = "mobile"
	// PreferredPlatformConsole indicates that the user's preferred platform is Xbox Console (Series X, One).
	PreferredPlatformConsole = "console"
)

var (
	// profileEndpoint is the base URL for the Xbox Live Profile API.
	// Profile API is primarily used to modify/query settings in the user's profile.
	//
	// Requests to this endpoint must include the 'X-Xbl-Contract-Version' header
	// set to '2'. Use the profileContractVersion request option for this purpose.
	profileEndpoint = &url.URL{
		Scheme: "https",
		Host:   "profile.xboxlive.com",
	}
	// profileContractVersion is an [internal.RequestOption] that sets an 'X-Xbl-Contract-Version'
	// header to '2' for requests made to the profileEndpoint.
	profileContractVersion = internal.ContractVersion("2")
)

// UploadProfileImage uploads a custom profile image for the caller using
// the given [MediaReader]. It is only functional when signed in to an Xbox app,
// otherwise an error will be returned.
func (c *Client) UploadProfileImage(ctx context.Context, mr MediaReader, opts ...internal.RequestOption) error {
	length := mr.Len()
	if length > maxProfileImageSize {
		return fmt.Errorf("social: profile image must not exceed 50 MiB: got %d bytes", length)
	}
	blockCount := (length + blockSize - 1) / blockSize
	uploadURL, publishURL, err := c.profileUploadURL(ctx, length, blockCount, opts)
	if err != nil {
		return fmt.Errorf("resolve upload URL: %w", err)
	}

	for index := 1; mr.Len() > 0; index++ {
		size := int64(min(blockSize, mr.Len()))
		block := io.LimitReader(mr, size)
		blockID := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("BlockId%07d", index)))

		if err := c.uploadBlock(ctx, uploadURL, blockID, size, block, opts); err != nil {
			return fmt.Errorf("upload block #%d: %w", index, err)
		}
	}

	req, err := internal.NewRequest(ctx, http.MethodPost, publishURL, nil, append(opts,
		mediaHubContractVersion,
		internal.RequestHeader("Accept", "application/json"),
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
	case http.StatusAccepted:
		return nil
	default:
		return internal.UnexpectedStatusCode(resp)
	}
}

const (
	// maxProfileImageSize is the maximum size of a profile image in bytes that is apparently
	// accepted by the Xbox Live Media Hub service.
	maxProfileImageSize = 50 * 1024 * 1024

	// blockSize is the maximum size of a single block. Media content larger
	// than this limit is split into multiple blocks when uploaded.
	//
	// Note that this limit is not based on a value observed from official apps,
	// as they now truncate images to a smaller size, making it impossible to
	// confirm the limit simply by attempting to upload a large image.
	// But it is technically possible to upload multiple blocks to Azure Blob Storage,
	// so we split the content into multiple blocks to optimize the upload.
	blockSize = 25 * 1024 * 1024
)

// MediaReader is an [io.Reader] that provides the total size of the media content.
//
// The following types implement this interface:
//   - [bytes.Buffer]
//   - [bytes.Reader]
type MediaReader interface {
	io.Reader

	// Len returns the total size of the media content in bytes.
	// It is used to determine the content length and the number of blocks
	// required to upload the content.
	Len() int
}

// uploadBlock uploads a single block using the specified URL and block ID.
// The block ID must be in the form 'BlockId%07d' encoded with base64.
// The reader should limit the amount of data read to the size of the block,
// typically by using [io.LimitReader] with [blockSize].
func (c *Client) uploadBlock(ctx context.Context, uploadURL string, blockID string, contentLength int64, reader io.Reader, opts []internal.RequestOption) error {
	u, err := url.Parse(uploadURL)
	if err != nil {
		return fmt.Errorf("upload URL: %w", err)
	}
	q := u.Query()
	q.Set("comp", "block")
	q.Set("blockId", blockID)
	u.RawQuery = q.Encode()

	req, err := internal.NewRequest(ctx, http.MethodPut, u.String(), reader, append(opts,
		internal.RequestHeader("Accept", "application/json"),
		internal.RequestHeader("x-ms-blob-type", "BlockBlob"),
		internal.RequestHeader("Content-Type", "application/octet-stream"),
	))
	if err != nil {
		return fmt.Errorf("make request: %w", err)
	}
	// Prevent HTTP client from guessing the length of the body.
	req.ContentLength = contentLength

	resp, err := c.client.Do(nsal.WithoutAuthHeaders(req))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
		return nil
	default:
		return internal.UnexpectedStatusCode(resp)
	}
}

// profileUploadURL creates a custom profile image upload session and returns
// the URLs for uploading and publishing the image.
//
// The upload URL is used to upload the image content to Blob Storage.
// The publish URL is used to commit the uploaded content as the caller's
// profile image.
func (c *Client) profileUploadURL(ctx context.Context, fileSize, blockCount int, opts []internal.RequestOption) (uploadURL, publishURL string, err error) {
	requestURL := mediaHubEndpoint.JoinPath("/customPics/create").String()
	req, err := internal.WithJSONBody(ctx, http.MethodPost, requestURL, map[string]any{
		"ExpectedBlocks": blockCount,
		"FileSize":       fileSize,
		"InitialMetadata": map[string]any{
			"CustomPicType": "Gamerpic",
			"AssociationId": c.userInfo.XUID,
		},
	}, append(opts,
		mediaHubContractVersion,
		internal.RequestHeader("Accept", "application/json"),
		internal.RequestHeader("Content-Type", "application/json"),
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
			ContentID  string `json:"contentId"`
			PublishURI string `json:"publishUri"`
			UploadURI  string `json:"contentUploadUri"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", "", fmt.Errorf("decode response body: %w", err)
		}
		if result.UploadURI == "" || result.PublishURI == "" {
			return "", "", errors.New("social: invalid create custom image result")
		}
		return result.UploadURI, result.PublishURI, nil
	default:
		return "", "", internal.UnexpectedStatusCode(resp)
	}
}

var (
	// mediaHubEndpoint is the base URL for the Xbox Live MediaHub API.
	// MediaHub is primarily used to upload and query media content uploaded to Xbox Live, including:
	//  - Screenshots
	//  - Clips
	//  - Custom profile images
	//
	// Requests to this endpoint must include the 'X-Xbl-Contract-Version' header
	// set to '3'. Use the mediaHubContractVersion request option for this purpose.
	mediaHubEndpoint = &url.URL{
		Scheme: "https",
		Host:   "mediahub.xboxlive.com",
	}

	// mediaHubContractVersion is an [internal.RequestOption] that sets an 'X-Xbl-Contract-Version'
	// header to '3' for requests made to the mediaHubEndpoint.
	mediaHubContractVersion = internal.ContractVersion("3")
)
