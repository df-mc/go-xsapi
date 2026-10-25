package notification

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestUnmarshalGameInviteOptions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		actions     string
		wantActions int
	}{
		{"inbox", `"Actions":[{"ActionId":"action-1"}]`, 1},
		{"websocket", `"Action":{"ActionId":"action-1"}`, 1},
		{"combined", `"Actions":[{"ActionId":"action-1"}],"Action":{"ActionId":"action-2"}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := []byte(fmt.Sprintf(`{
				"SubscriptionCategory":"Microsoft.Xbox.Multiplayer",
				"SubscriptionType":"GameInvites",
				"SubscriptionId":"invite-1",
				%s,
				"NotificationOptions":{
					"Location":{"Id":"1739947436","Name":"Minecraft"},
					"Platforms":["android","uwp-desktop"]
				}
			}`, tc.actions))
			n, err := Unmarshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			invite, ok := n.(*GameInvite)
			if !ok {
				t.Fatalf("notification type = %T, want *GameInvite", n)
			}
			if invite.Options.Location.ID != "1739947436" {
				t.Fatalf("location ID = %q, want 1739947436", invite.Options.Location.ID)
			}
			if got := invite.Options.Platforms; len(got) != 2 || got[0] != "android" || got[1] != "uwp-desktop" {
				t.Fatalf("platforms = %v, want [android uwp-desktop]", got)
			}
			if invite.SubscriptionID() != "invite-1" || len(invite.Actions) != tc.wantActions {
				t.Fatalf("shared fields lost: id=%q actions=%d", invite.SubscriptionID(), len(invite.Actions))
			}
			var direct GameInvite
			if err := json.Unmarshal(wire, &direct); err != nil {
				t.Fatal(err)
			}
			if direct.Options.Location.ID != invite.Options.Location.ID || len(direct.Actions) != tc.wantActions {
				t.Fatal("direct JSON decoding lost options or actions")
			}
		})
	}
}

func TestUnmarshalGameInviteRequiresActions(t *testing.T) {
	_, err := Unmarshal([]byte(`{"SubscriptionCategory":"Microsoft.Xbox.Multiplayer","SubscriptionType":"GameInvites","NotificationOptions":{"Location":{"Id":"1739947436"}}}`))
	if err == nil {
		t.Fatal("expected an error for an invite without actions")
	}
}

func TestUnmarshalRelationshipWithoutOptions(t *testing.T) {
	for _, typ := range []string{SubscriptionTypeFollowers, SubscriptionTypeAcceptedFriendRequests, SubscriptionTypeIncomingFriendRequests} {
		t.Run(typ, func(t *testing.T) {
			wire := []byte(fmt.Sprintf(`{"SubscriptionCategory":"Microsoft.Xbox.People","SubscriptionType":%q,"SubscriptionId":"relationship-1","Action":{"ActionId":"action-1"}}`, typ))
			n, err := Unmarshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			if n.SubscriptionType() != typ || n.SubscriptionID() != "relationship-1" {
				t.Fatalf("unexpected notification: %#v", n)
			}
		})
	}
}
