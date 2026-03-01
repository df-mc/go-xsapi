package chat

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

type ClientEnvelope struct {
	Channel  *Channel `json:"channel,omitempty"`
	Type     string   `json:"messageType"`
	Sequence uint32   `json:"clientSeqNum"`
}

func (message *ClientEnvelope) prepareEnvelope(messageType string, seq uint32) {
	message.Type, message.Sequence = messageType, seq
}

type clientRequest interface {
	prepareEnvelope(messageType string, seq uint32)
	MessageType() string
}

const (
	MessageTypeWhoAmI = "WhoAmI"
	MessageTypeNoOp   = "NoOp"
)

type Channel struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

const (
	ChannelTypeSystem            = "System"
	ChannelTypeMessage           = "XboxMessage"
	ChannelTypeGroupMessage      = "GroupMessage" // NOTE: Add .0 to ID
	ChannelTypeNotificationInbox = "NotificationInbox"
)

type whoAmIRequest struct {
	*ClientEnvelope
}

func (r whoAmIRequest) MessageType() string {
	return MessageTypeWhoAmI
}

type ServerEnvelope struct {
	Sequence       uint32    `json:"clientSeqNum"`
	Timestamp      time.Time `json:"messageTime"`
	Type           string    `json:"messageType"`
	ID             string    `json:"messageId"`
	SenderXUID     string    `json:"senderXuid"`
	SenderGamerTag string    `json:"senderGamertag"`
	Channel        Channel   `json:"channel"`

	// ServerOriginated is true when the message is not originated
	// by the client, i.e. not caused by a client message with valid sequence.
	ServerOriginated bool `json:"flagServerOriginated"`

	// Raw is used for decoding the embedded payload.
	// Implementations should define a struct that
	// defines the payload that contains the fields
	// which doesn't exist in ServerEnvelope.
	// It is set in [ServerEnvelope.UnmarshalJSON]
	// during decode.
	Raw json.RawMessage `json:"-"`
}

type whoAmIResult struct {
	ConnectionID uuid.UUID `json:"connectionId"`
	ServerNonce  string    `json:"serverNonce"`
}

func (p *whoAmIResult) UnmarshalJSON(b []byte) error {
	type Alias whoAmIResult
	if err := json.Unmarshal(b, (*Alias)(p)); err != nil {
		return err
	}
	if p.ConnectionID == uuid.Nil {
		return errors.New("xsapi/chat: whoAmIResult.ID is nil")
	}
	if p.ServerNonce == "" {
		return errors.New("xsapi/chat: whoAmIResult.ServerNonce is empty")
	}
	return nil
}

func (e *ServerEnvelope) UnmarshalJSON(b []byte) error {
	type Alias ServerEnvelope
	e.Raw = b
	return json.Unmarshal(b, (*Alias)(e))
}
