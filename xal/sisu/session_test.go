package sisu

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi"
	"github.com/df-mc/go-xsapi/mpsd"
	"github.com/df-mc/go-xsapi/xal"
	"github.com/df-mc/go-xsapi/xal/xasd"
	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

func TestSession(t *testing.T) {
	if err := os.MkdirAll(testdataDir, os.ModePerm); err != nil {
		t.Fatalf("error making parent directories for %q: %s", testdataDir, err)
	}
	msa := MinecraftAndroid.TokenSource(t.Context(), msaToken(t, tokenPath))
	t.Cleanup(func() {
		token, err := msa.Token()
		if err != nil {
			t.Errorf("error requesting Microsoft Account token: %s", err)
			return
		}
		b, err := json.Marshal(token)
		if err != nil {
			t.Errorf("error encoding Microsoft Account token for saving: %s", err)
			return
		}
		if err := os.WriteFile(tokenPath, b, os.ModePerm); err != nil {
			t.Errorf("error writing Microsoft Account token to %s: %s", tokenPath, err)
			return
		}
		t.Logf("cleanup: saved Microsoft Account token to %s", tokenPath)
	})

	dt, proofKey := readDevice(t, deviceSnapshotPath)
	deviceSource := xasd.ReuseTokenSource(MinecraftAndroid.Config, dt, proofKey)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
		defer cancel()
		token, err := deviceSource.DeviceToken(ctx)
		if err != nil {
			t.Fatalf("error requesting device token: %s", err)
		}
		writeDevice(t, deviceSnapshotPath, token, deviceSource.ProofKey())
		t.Logf("cleanup: saved device to: %s", deviceSnapshotPath)
	})

	sc := &SessionConfig{DeviceTokenSource: deviceSource}
	sc.Snapshot = readSnapshot(t, snapshotPath)
	s := MinecraftAndroid.New(msa, sc)
	t.Cleanup(func() {
		cache := s.Snapshot()
		if cache == nil {
			t.Fatal("Session.Snapshot must return non-nil SessionState")
		}
		writeSnapshot(t, snapshotPath, cache)
		t.Logf("cleanup: written session snapshot")
	})

	device, err := s.DeviceToken(tokenContext(t))
	if err != nil {
		t.Fatalf("error requesting XASD token: %s", err)
	}
	t.Logf("device token: %#v", device)
	title, err := s.TitleToken(tokenContext(t))
	if err != nil {
		t.Fatalf("error requesting XAST token: %s", err)
	}
	t.Logf("title token: %#v", title)
	user, err := s.UserToken(tokenContext(t))
	if err != nil {
		t.Fatalf("error requesting XASU token: %s", err)
	}
	t.Logf("user token: %#v", user)

	xsts, err := s.XSTSToken(tokenContext(t), playFabRelyingParty)
	if err != nil {
		fmt.Println(err)
		t.Fatalf("error requesting XSTS token for %q: %s", playFabRelyingParty, err)
	}
	t.Logf("XSTS token for %q: %#v", playFabRelyingParty, xsts)

	// go publishSession(t, s)
	publishSession(t, s)
}

func publishSession(t testing.TB, src *Session) {
	client, err := xsapi.NewClient(src, &xsapi.ClientConfig{})
	if err != nil {
		t.Fatalf("error creating API client: %s", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()

	t.Logf("logged in as %s (%s)", client.UserInfo().GamerTag, client.UserInfo().XUID)

	addFriend(t, client, "2535428765332540")

	custom, err := json.Marshal(map[string]any{
		"Joinability":             "joinable_by_friends",
		"hostName":                client.UserInfo().GamerTag,
		"ownerId":                 client.UserInfo().XUID,
		"rakNetGUID":              "",
		"version":                 "1.21.132",
		"levelId":                 "opSQE3ZX5Yc=",
		"worldName":               "マイ ワールド",
		"worldType":               "Creative",
		"protocol":                898,
		"MemberCount":             1,
		"MaxMemberCount":          8,
		"BroadcastSetting":        3,
		"LanGame":                 true,
		"isEditorWorld":           false,
		"isHardcore":              false,
		"TransportLayer":          2,
		"OnlineCrossPlatformGame": true,
		"CrossPlayDisabled":       false,
		"TitleId":                 0,
		"SupportedConnections": []map[string]any{
			{
				"ConnectionType": 7,
				"HostIpAddress":  "",
				"HostPort":       0,
				"NetherNetId":    uint64(15831069647900212779),
				"PmsgId":         "0ebaf7bc-b99e-23df-56b9-17c650aa2584",
			},
		},
	})
	if err != nil {
		t.Fatalf("error encoding custom properties: %s", err)
	}
	session, err := client.MPSD().Publish(ctx, mpsd.SessionReference{
		ServiceConfigID: serviceConfigID,
		TemplateName:    "MinecraftLobby",
	}, mpsd.PublishConfig{
		CustomProperties: custom,
	})
	if err != nil {
		t.Fatalf("error publishing multiplayer session: %s", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("error closing multiplayer session: %s", err)
		}
		t.Logf("cleanup: session closed")
	})

	<-time.After(time.Second * 5)
}

func addFriend(t testing.TB, c *xsapi.Client, xuid string) {
	requestURL := fmt.Sprintf("https://social.xboxlive.com/users/xuid(%s)/people/friends/v2/xuid(%s)", c.UserInfo().XUID, xuid)
	req, err := http.NewRequest(http.MethodPut, requestURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-xbl-contract-version", "3")

	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", req.URL, resp.Status)
	}
	b, _ := io.ReadAll(resp.Body)
	t.Log(string(b))
}

func appearOnline(t testing.TB, c *xsapi.Client) {

}

func readSnapshot(t testing.TB, path string) *Snapshot {
	if stat, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if stat.IsDir() {
		t.Fatalf("%q is a directory", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("error reading session cache: %s", path)
	}
	var snapshot *Snapshot
	if err := json.Unmarshal(b, &snapshot); err != nil {
		t.Fatalf("error decoding session cache: %s", err)
	}
	return snapshot
}

func writeSnapshot(t testing.TB, path string, cache *Snapshot) {
	b, err := json.Marshal(cache)
	if err != nil {
		t.Fatalf("error encoding Snapshot: %s", err)
	}
	if err := os.WriteFile(path, b, os.ModePerm); err != nil {
		t.Fatalf("error writing session snapshot to %s: %s", path, err)
	}
	t.Logf("Session.Snapshot: %s", b)
}

func msaToken(t testing.TB, path string) *oauth2.Token {
	if stat, err := os.Stat(path); os.IsNotExist(err) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second*15)
		defer cancel()
		da, err := MinecraftAndroid.DeviceAuth(ctx)
		if err != nil {
			t.Fatalf("error requesting device authentication flow: %s", err)
		}
		t.Logf("Sign in to Microsoft Account at %s using the code %s. You have 1 minute to sign in.", da.VerificationURI, da.UserCode)

		ctx, cancel = context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		msa, err := MinecraftAndroid.DeviceAccessToken(ctx, da)
		if err != nil {
			t.Fatalf("error polling device authentication flow for access token: %s", err)
		}
		b, err := json.Marshal(msa)
		if err != nil {
			t.Fatalf("error encoding oauth2 token: %s", err)
		}
		if err := os.WriteFile(path, b, os.ModePerm); err != nil {
			t.Fatalf("error writing oauth2 token to %s: %s", path, err)
		}
		return msa
	} else if err != nil {
		t.Fatalf("stat %q: %s", path, err)
	} else if stat.IsDir() {
		t.Fatalf("%q is a directory", path)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("error reading token cache: %s", err)
	}
	var msa *oauth2.Token
	if err := json.Unmarshal(b, &msa); err != nil {
		t.Fatalf("error decoding oauth2 token from cache: %s", err)
	}
	return msa
}

func tokenContext(t testing.TB) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second*15)
	t.Cleanup(cancel)
	return ctx
}

const (
	testdataDir = "testdata"

	playFabRelyingParty = "https://b980a380.minecraft.playfabapi.com/"
)

var (
	snapshotPath = filepath.Join(testdataDir, "session.snapshot")
	tokenPath    = filepath.Join(testdataDir, "msa.token")

	MinecraftAndroid = Config{
		Config: xal.Config{
			Device: xal.Device{
				Type:    xal.DeviceTypeAndroid,
				Version: "13",
			},
			UserAgent: "XAL Android 2025.04.20250326.000",
			TitleID:   1739947436,
		},

		ClientID:    "0000000048183522",
		RedirectURI: "ms-xal-0000000048183522://auth",
		Sandbox:     "RETAIL",
	}

	serviceConfigID = uuid.MustParse("4fc10100-5f7a-4470-899b-280835760c07")
)

func readDevice(t testing.TB, path string) (*xasd.Token, *ecdsa.PrivateKey) {
	if stat, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		t.Fatalf("stat %q: %s", path, err)
	} else if stat.IsDir() {
		t.Fatalf("%q is a directory", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("error reading device snapshot: %s", err)
	}
	var snapshot *deviceTokenSnapshot
	if err := json.Unmarshal(b, &snapshot); err != nil {
		t.Fatalf("error decoding device snapshot: %s", err)
	}
	return snapshot.DeviceToken, snapshot.ProofKey.Key.(*ecdsa.PrivateKey)
}

func writeDevice(t testing.TB, path string, token *xasd.Token, proofKey *ecdsa.PrivateKey) {
	b, err := json.Marshal(&deviceTokenSnapshot{
		ProofKey: jose.JSONWebKey{
			Key:       proofKey,
			Algorithm: string(jose.ES256),
			Use:       "sig",
		},
		DeviceToken: token,
	})
	if err != nil {
		t.Fatalf("error encoding device token snapshot: %s", err)
	}
	if err := os.WriteFile(path, b, os.ModePerm); err != nil {
		t.Fatalf("error writing device token snapshot: %s", err)
	}
}

type deviceTokenSnapshot struct {
	ProofKey    jose.JSONWebKey
	DeviceToken *xasd.Token
}

var (
	deviceSnapshotPath = filepath.Join(testdataDir, "device.token")
)
