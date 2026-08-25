package chat

import (
	"encoding/json"
	"fmt"
	"strconv"
)

type (
	MessageContent interface {
		ContentType() string
		ContentVersion() int
		String() string
	}

	TextContent struct {
		Text string `json:"text"`

		UnsuitableFor []string `json:"unsuitableFor,omitempty"`
	}
	DeepLinkContent struct {
		ButtonText string  `json:"buttonText"`
		AppURI     *string `json:"appUri,omitzero"`
		WebURI     *string `json:"webUri,omitzero"`
	}
	ImageContent struct {
		// AttachmentID is the ID associated with the underlying attachment of the image.
		AttachmentID string `json:"attachmentId"`
		// FileType indicates the type of the image, e.g. PNG or JPEG.
		FileType string `json:"filetype"`
		// Size is the total size of the file in bytes.
		Size int64 `json:"sizeInBytes,omitzero"`
		// Hash is the MD5 hash for the image content.
		Hash []byte `json:"hash,omitzero"`
		// Height is the height of the image.
		Height int `json:"height,omitzero"`
		// Width is the width of the image.
		Width int `json:"width,omitzero"`

		// DownloadURI is the URI for downloading the image content.
		DownloadURI   string   `json:"downloadUri,omitempty"`
		UnsuitableFor []string `json:"unsuitableFor,omitempty"`
	}
)

const (
	ContentTypeText     = "text"
	ContentTypeDeepLink = "deeplink"
	ContentTypeImage    = "image"
)

func (*TextContent) ContentType() string {
	return ContentTypeText
}

func (*TextContent) ContentVersion() int {
	return 0
}

func (t *TextContent) String() string {
	return t.Text
}

func (t *TextContent) MarshalJSON() ([]byte, error) {
	type Alias TextContent
	return json.Marshal(struct {
		messageContentKey
		*Alias
	}{
		messageContentKey: messageContentKey{
			ContentType: t.ContentType(),
			Version:     t.ContentVersion(),
		},
		Alias: (*Alias)(t),
	})
}

func (*DeepLinkContent) ContentType() string {
	return ContentTypeDeepLink
}

func (*DeepLinkContent) ContentVersion() int {
	return 0
}

func (d *DeepLinkContent) String() string {
	u := d.WebURI
	if u == nil {
		if u = d.AppURI; u == nil {
			return d.ButtonText
		}
	}
	return d.ButtonText + " (" + *u + ")"
}

func (d *DeepLinkContent) MarshalJSON() ([]byte, error) {
	type Alias DeepLinkContent
	return json.Marshal(struct {
		messageContentKey
		*Alias
	}{
		messageContentKey: messageContentKey{
			ContentType: d.ContentType(),
			Version:     d.ContentVersion(),
		},
		Alias: (*Alias)(d),
	})
}

func (*ImageContent) ContentType() string {
	return ContentTypeImage
}

func (*ImageContent) ContentVersion() int {
	return 0
}

func (i *ImageContent) String() string {
	return fmt.Sprintf("Image(url=%s, height=%d, width=%d)", i.DownloadURI, i.Height, i.Width)
}

func (i *ImageContent) MarshalJSON() ([]byte, error) {
	type Alias ImageContent
	return json.Marshal(struct {
		messageContentKey
		*Alias
	}{
		messageContentKey: messageContentKey{
			ContentType: i.ContentType(),
			Version:     i.ContentVersion(),
		},
		Alias: (*Alias)(i),
	})
}

type UnknownContent struct {
	Type    string `json:"contentType"`
	Version int    `json:"version"`

	Raw []byte `json:"-"`
}

func (c *UnknownContent) ContentType() string {
	return c.Type
}

func (c *UnknownContent) ContentVersion() int {
	return c.Version
}

func (c *UnknownContent) String() string {
	return fmt.Sprintf("unknown content with type: %q", c.Type)
}

func (c *UnknownContent) UnmarshalJSON(b []byte) error {
	type Alias UnknownContent
	if err := json.Unmarshal(b, (*Alias)(c)); err != nil {
		return err
	}
	c.Raw = b
	return nil
}

var MessageContentPool = newPool[messageContentKey, MessageContent](func() MessageContent { return &UnknownContent{} })

type messageContentKey struct {
	ContentType string `json:"contentType"`
	Version     int    `json:"version"`
}

func (k messageContentKey) Type() string {
	return k.ContentType + "/" + strconv.Itoa(k.Version)
}

func init() {
	register := func(typ string, version int, f func() MessageContent) {
		MessageContentPool.Register(typ+"/"+strconv.Itoa(version), f)
	}

	register(ContentTypeText, 0, func() MessageContent { return &TextContent{} })
	register(ContentTypeDeepLink, 0, func() MessageContent { return &DeepLinkContent{} })
}
