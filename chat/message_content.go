package chat

import "fmt"

type (
	MessageContent interface {
		ContentType() string
		String() string
	}

	TextContent struct {
		messageContent
		Text          string   `json:"text"`
		UnsuitableFor []string `json:"unsuitableFor"`
	}
	DeepLinkContent struct {
		messageContent
		ButtonText string  `json:"buttonText"`
		AppURI     *string `json:"appUri"`
		WebURI     *string `json:"webUri"`
	}

	messageContentKey struct {
		ContentType string `json:"contentType"`
	}
	messageContent struct {
		Type    string `json:"contentType"`
		Version int    `json:"version"`
	}
)

func (k messageContentKey) Type() string {
	return k.ContentType
}

const (
	ContentTypeText     = "text"
	ContentTypeDeepLink = "deeplink"
)

func (t *TextContent) ContentType() string {
	return ContentTypeText
}

func (t *TextContent) String() string {
	return t.Text
}

func (d *DeepLinkContent) ContentType() string {
	return ContentTypeDeepLink
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

type UnknownContent struct {
	Type string `json:"contentType"`
}

func (c *UnknownContent) ContentType() string {
	return c.Type
}

func (c *UnknownContent) String() string {
	return fmt.Sprintf("unknown content with type: %q", c.Type)
}

var MessageContentPool = newPool[messageContentKey, MessageContent](func() MessageContent { return &UnknownContent{} })

func init() {
	MessageContentPool.Register(ContentTypeText, func() MessageContent { return &TextContent{} })
	MessageContentPool.Register(ContentTypeDeepLink, func() MessageContent { return &DeepLinkContent{} })
}
