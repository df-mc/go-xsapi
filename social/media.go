package social

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/df-mc/go-xsapi/v2/internal"
	"github.com/df-mc/go-xsapi/v2/xal/nsal"
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
