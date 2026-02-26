package sisu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/df-mc/go-xsapi"
	"github.com/google/uuid"
)

func testNotification(t testing.TB, client *xsapi.Client) {
	conn, _, err := websocket.Dial(shortContext(t), "wss://chat.xboxlive.com/chat/connect", &websocket.DialOptions{
		HTTPClient:   client.HTTPClient(),
		Subprotocols: []string{"chat"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := newChatConn(t, conn)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("error closing connection: %s", err)
		}
	})

	// client.PushNotification().Handle(h notificationinbox.Hand

	// chat.NotificationHandler

	// chat.NotificationHandler.HandleGameInvite()

	// Chat chat.Config.DisableNotificationInbox

	// if err := client.Chat().NotificationHistory(ctx, chat.NotificationFilter{}); err != nil {}

	// DisableNotification: true,

	// chat.NotificationHandler.HandleGameInvite()
	// chat.NotificationHandler.HandleIncomingFriendRequests(ctx)

	// client.Chat().NotificationInbox().Handle(h NotificationHandler)
	// client.Chat().JoinChannel(ctx, chat.Channel{
	// 		Type: chat.ChannelTypeXboxMessage,
	// 		ID: xuid,
	// })
	// channel, err := client.Chat().JoinChannel()
	// if t := channel.Ticket(); t != nil {
	//

	// if err := client.inbox.Handle(message); err != nil {}

	// if err := client.Notification().Handle(

	// if err := client.Notification().Handle()

	// notification.go

	// HandleGameInvite(ctx, *chat.GameInviteNotification)

	// HandleNotification

	// *chat.GameInviteNotification
	// *chat.IncomingRequestsNotification
	// *chat.ClaimRewardsNotification

	// *chat.Notification

	// if notification.Action != nil {}

	connectionID, nonce, err := c.WhoAmI(shortContext(t))
	if err != nil {
		t.Fatalf("error calling WhoAmI: %s", err)
	}
	t.Logf("%s %q", connectionID, nonce)
	registerNotificationInbox(t, client, connectionID, nonce)

	time.Sleep(time.Minute * 2)
}

func registerNotificationInbox(t testing.TB, client *xsapi.Client, connectionID uuid.UUID, nonce string) {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(map[string]any{
		"ChatConnectionId": connectionID,
		"ChatNonce":        nonce,
	}); err != nil {
		t.Fatalf("error encoding request body: %s", err)
	}

	requestURL := "https://notificationinbox.xboxlive.com/users/" + client.UserInfo().XUID + "/inbox/push?subscriptionCategory=Microsoft.Xbox.Multiplayer,Microsoft.Xbox.People,Microsoft.Xbox.Rewards&subscriptionType=GameInvites,GamePartyInvites,GamePartyInvitesWithoutHandles,MultiplayerActivityGameInvites,PartyInvites,Followers,AcceptedFriendRequests,IncomingFriendRequests,ClaimReminder"
	req, err := http.NewRequestWithContext(shortContext(t), http.MethodPost, requestURL, buf)
	if err != nil {
		t.Fatalf("error making request: %s", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-xbl-contract-version", "1")

	resp, err := client.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}

func newChatConn(t testing.TB, conn *websocket.Conn) *chatConn {
	c := &chatConn{
		conn: conn,
		t:    t,

		expected: make(map[int32]*handshake),
	}
	c.ctx, c.cancel = context.WithCancelCause(context.Background())
	go c.background()
	return c
}

type chatConn struct {
	conn *websocket.Conn
	t    testing.TB

	seq        atomic.Int32
	expected   map[int32]*handshake
	expectedMu sync.Mutex

	ctx    context.Context
	cancel context.CancelCauseFunc
	once   sync.Once
}

func (c *chatConn) shake(sequence int32, payload clientMessage) (<-chan *serverMessage, error) {
	payload.ClientSeqNum = sequence
	if err := c.write(payload); err != nil {
		return nil, err
	}
	hand := make(chan *serverMessage)
	c.expectedMu.Lock()
	c.expected[payload.ClientSeqNum] = &handshake{
		ch:  hand,
		typ: payload.MessageType,
	}
	c.expectedMu.Unlock()
	return hand, nil
}

func (c *chatConn) release(sequence int32) {
	c.expectedMu.Lock()
	delete(c.expected, sequence)
	c.expectedMu.Unlock()
}

type handshake struct {
	ch  chan<- *serverMessage
	typ string
}

func (c *chatConn) write(payload any) error {
	return wsjson.Write(context.Background(), c.conn, payload)
}

func (c *chatConn) WhoAmI(ctx context.Context) (id uuid.UUID, nonce string, err error) {
	seq := c.seq.Add(1)
	hand, err := c.shake(seq, clientMessage{
		Channel: &channel{
			Type: "System",
		},
		MessageType: "WhoAmI",
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	defer c.release(seq)

	select {
	case <-c.ctx.Done():
		return uuid.Nil, "", context.Cause(c.ctx)
	case <-ctx.Done():
		return uuid.Nil, "", ctx.Err()
	case msg := <-hand:
		if msg.ConnectionID == uuid.Nil || msg.ServerNonce == "" {
			return uuid.Nil, "", fmt.Errorf("invalid message: %#v", msg)
		}
		return msg.ConnectionID, msg.ServerNonce, nil
	}
}

func (c *chatConn) background() {
	for {
		_, msg, err := c.conn.Read(c.ctx)
		if err != nil {
			_ = c.close(err)
			return
		}

		var message *serverMessage
		if err := json.Unmarshal(msg, &message); err != nil {
			c.t.Errorf("error decoding WebSocket message: %s: %s", msg, err)
			continue
		}
		if message.MessageType == "NoOp" {
			continue
		}

		if message.ClientSeqNum == 0 {
			c.t.Log("→", string(msg))
			continue
		}

		c.expectedMu.Lock()
		hand, ok := c.expected[message.ClientSeqNum]
		if !ok {
			c.expectedMu.Unlock()
			c.t.Errorf("unexpected seq num: %d", message.ClientSeqNum)
		}
		c.expectedMu.Unlock()

		if hand.typ != message.MessageType {
			c.t.Errorf("unexpected message type: %q", message.MessageType)
		}
		hand.ch <- message
	}
}

func (c *chatConn) Close() (err error) {
	return c.close(net.ErrClosed)
}

func (c *chatConn) close(cause error) (err error) {
	c.once.Do(func() {
		c.cancel(cause)
		err = c.conn.Close(websocket.StatusNormalClosure, "")
	})
	return err
}

type serverMessage struct {
	ClientSeqNum         int32     `json:"clientSeqNum"`
	MessageTime          time.Time `json:"messageTime"`
	MessageType          string    `json:"messageType"`
	MessageID            string    `json:"messageId"`
	SenderXUID           string    `json:"senderXuid"`
	SenderGamerTag       string    `json:"senderGamerTag"`
	Channel              channel   `json:"channel"`
	FlagServerOriginated bool      `json:"flagServerOriginated"`

	ConnectionID uuid.UUID `json:"connectionId"`
	ServerNonce  string    `json:"serverNonce"`
}

type clientMessage struct {
	Channel      *channel `json:"channel,omitempty"`
	MessageType  string   `json:"messageType"`
	ClientSeqNum int32    `json:"clientSeqNum"`
}

type channel struct {
	Type string `json:"type"` // System
}

func shortContext(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	t.Cleanup(cancel)
	return ctx
}
