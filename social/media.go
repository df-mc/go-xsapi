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
)

func (c *Client) UploadProfileImage(ctx context.Context, mr MediaReader, opts ...internal.RequestOption) error {
	blockCount := (mr.Len()/blockSize - 1) / blockSize
	uploadURL, publishURL, err := c.profileUploadURL(ctx, mr.Len(), blockCount, opts)
	if err != nil {
		return fmt.Errorf("resolve upload URL: %w", err)
	}

	for index := 0; mr.Len() > 0; index++ {
		size := int64(min(blockSize, mr.Len()))
		block := io.LimitReader(mr, size)
		blockID := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("BlockId%07d", index)))

		if err := c.uploadBlock(ctx, uploadURL, blockID, block, opts); err != nil {
			return fmt.Errorf("upload block #%d: %w", index, err)
		}
	}

	if err := c.publishCustomImage(ctx, publishURL, opts); err != nil {
		return fmt.Errorf("publish custom image: %w", err)
	}
	return nil
}

const blockSize = 32768

func (c *Client) publishCustomImage(ctx context.Context, requestURL string, opts []internal.RequestOption) error {
	req, err := internal.NewRequest(ctx, http.MethodPost, requestURL, nil, append(opts,
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

type MediaReader interface {
	io.Reader
	Len() int
}

func (c *Client) uploadBlock(ctx context.Context, uploadURL string, blockID string, reader io.Reader, opts []internal.RequestOption) error {
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
			UploadURI  string `json:"uploadUri"`
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
	mediaHubEndpoint = &url.URL{
		Scheme: "https",
		Host:   "mediahub.xboxlive.com",
	}
	mediaHubContractVersion = internal.ContractVersion("3")
)
