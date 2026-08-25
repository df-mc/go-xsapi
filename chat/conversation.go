package chat

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

type Conversation struct {
	conversation

	Messages []Message `json:"messages"`
}

func (c *Conversation) UnmarshalJSON(b []byte) error {
	type Alias Conversation
	data := struct {
		*Alias
		Messages []json.RawMessage `json:"messages"`
	}{Alias: (*Alias)(c)}
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	c.Messages = make([]Message, len(data.Messages))
	for i, data := range data.Messages {
		var err error
		c.Messages[i], err = MessagePool.Decode(data)
		if err != nil {
			return err
		}
	}
	return nil
}

type ConversationResult struct {
	Conversation
	ContinuationToken string `json:"continuationToken"`
}

type ConversationSummary struct {
	conversation

	LastMessage Message `json:"lastMessage"`
}

func (c *ConversationSummary) UnmarshalJSON(b []byte) error {
	type Alias ConversationSummary
	data := struct {
		*Alias
		LastMessage json.RawMessage `json:"lastMessage"`
	}{Alias: (*Alias)(c)}
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	var err error
	c.LastMessage, err = MessagePool.Decode(data.LastMessage)
	if err != nil {
		return fmt.Errorf("chat: decode Conversation.LastMessage: %w", err)
	}
	return nil
}

type conversation struct {
	Timestamp     time.Time `json:"timestamp"`
	NetworkID     string    `json:"networkId"`
	Type          string    `json:"type"`
	ID            string    `json:"conversationId"`
	VoiceID       string    `json:"voiceId"`
	Participants  []string  `json:"participants"`
	ReadHorizon   string    `json:"readHorizon"`
	DeleteHorizon string    `json:"deleteHorizon"`
	MarkedRead    bool      `json:"isRead"`
	Muted         bool      `json:"muted"`
	Folder        string    `json:"folder"`
}

func (c conversation) UserID(selfXUID string) (participantID string, _ error) {
	if c.Type != ConversationTypeUser {
		return "", fmt.Errorf("chat: Conversation.UserID can only be used within user conversation")
	}
	index := slices.IndexFunc(c.Participants, func(s string) bool {
		return s != selfXUID
	})
	if len(c.Participants) != 2 || index == -1 {
		return "", fmt.Errorf("chat: invalid Conversation.Participants: %s", strings.Join(c.Participants, ", "))
	}
	return c.Participants[index], nil
}

const (
	ConversationTypeUser  = "OneToOne"
	ConversationTypeGroup = "Group"
)
