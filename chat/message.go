package chat

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type Message interface {
	MessageType() string
	MessageID() string
	MessageClock() string
}

type ContentMessage struct {
	message

	Payload ContentMessagePayload `json:"contentPayload"`
}

func (*ContentMessage) MessageType() string {
	return MessageTypeContent
}

type AddGroupParticipants struct {
	message

	Payload AddGroupParticipantsPayload `json:"addGroupParticipantsPayload"`
}

func (*AddGroupParticipants) MessageType() string {
	return MessageTypeAddGroupParticipants
}

type AddGroupParticipantsPayload struct {
	UsersAdded    []string `json:"usersAdded"`
	UsersInvited  []string `json:"usersInvited"`
	AddFromInvite bool     `json:"addFromInvite"`
}

type ContentMessagePayload struct {
	Parts []MessageContent `json:"parts"`
}

func (c *ContentMessagePayload) UnmarshalJSON(b []byte) error {
	type Alias ContentMessagePayload
	var data struct {
		Content struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	parts := make([]MessageContent, len(data.Content.Parts))
	for i := range data.Content.Parts {
		var err error
		parts[i], err = MessageContentPool.Decode(data.Content.Parts[i])
		if err != nil {
			return err
		}
	}
	c.Parts = parts
	return nil
}

type ChangeGroupNameMessage struct {
	message
	Payload ChangeGroupNamePayload `json:"changeGroupNamePayload"`
}

func (c *ChangeGroupNameMessage) MessageType() string {
	return MessageTypeChangeGroupName
}

type ChangeGroupNamePayload struct {
	NewName string `json:"newName"`
}

type message struct {
	Timestamp           time.Time `json:"timestamp"`
	LastUpdateTimestamp time.Time `json:"lastUpdateTimestamp"`
	Type                string    `json:"type"`
	NetworkID           string    `json:"networkId"`
	ConversationType    string    `json:"conversationType"`
	ConversationID      string    `json:"conversationId"`
	Sender              string    `json:"sender"`
	ID                  string    `json:"messageId"`
	Clock               string    `json:"clock"`
	Deleted             bool      `json:"isDeleted"`
	ServerUpdated       bool      `json:"isServerUpdated"`
}

func (m *message) MessageID() string {
	return m.ID
}

func (m *message) MessageClock() string {
	return m.Clock
}

const (
	MessageTypeContent              = "ContentMessage"
	MessageTypeAddGroupParticipants = "AddGroupParticipants"
	MessageTypeChangeGroupName      = "ChangeGroupName"
)

type messageKey struct {
	MessageType string `json:"type"`
}

func (k messageKey) Type() string {
	return k.MessageType
}

type UnknownMessage struct {
	Type  string `json:"type"`
	ID    string `json:"messageId"`
	Clock string `json:"clock"`

	Raw []byte `json:"-"`
}

func (m *UnknownMessage) MessageType() string {
	return m.Type
}

func (m *UnknownMessage) MessageID() string {
	return m.ID
}

func (m *UnknownMessage) MessageClock() string {
	return m.Clock
}

func (m *UnknownMessage) UnmarshalJSON(b []byte) error {
	type Alias UnknownMessage
	if err := json.Unmarshal(b, (*Alias)(m)); err != nil {
		return err
	}
	m.Raw = b
	return nil
}

var MessagePool = newPool[messageKey, Message](func() Message { return &UnknownMessage{} })

func init() {
	MessagePool.Register(MessageTypeContent, func() Message { return &ContentMessage{} })

	MessagePool.Register(MessageTypeAddGroupParticipants, func() Message { return &AddGroupParticipants{} })
	MessagePool.Register(MessageTypeChangeGroupName, func() Message { return &ChangeGroupNameMessage{} })
}

func newPool[K poolKey, T any](unknown func() T) *pool[K, T] {
	return &pool[K, T]{
		m:       make(map[string]func() T),
		unknown: unknown,
	}
}

type pool[K poolKey, T any] struct {
	m       map[string]func() T
	unknown func() T
	mu      sync.RWMutex
}

type poolKey interface {
	Type() string
}

func (p *pool[K, T]) Register(typ string, f func() T) {
	p.mu.Lock()
	p.m[typ] = f
	p.mu.Unlock()
}

func (p *pool[K, T]) Decode(data []byte) (value T, err error) {
	var k K
	if err := json.Unmarshal(data, &k); err != nil {
		return value, fmt.Errorf("decode key: %w", err)
	}
	p.mu.RLock()
	f, ok := p.m[k.Type()]
	p.mu.RUnlock()
	if !ok {
		if p.unknown != nil {
			value = p.unknown()
		} else {
			return value, fmt.Errorf("unknown data with type: %q", k.Type())
		}
	} else {
		value = f()
	}
	if err := json.Unmarshal(data, value); err != nil {
		return value, fmt.Errorf("decode payload: %w", err)
	}
	return value, nil
}
