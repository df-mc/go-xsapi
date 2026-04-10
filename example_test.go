package xsapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/df-mc/go-xsapi/v2/presence"
	"github.com/df-mc/go-xsapi/v2/xal"
	"github.com/df-mc/go-xsapi/v2/xal/sisu"
)

// ExampleClient demonstrates how we can communicate with various Xbox Live network services.
// It uses the SISU configuration dumped from Minecraft: Bedrock Edition for Android for authentication.
func ExampleClient() {
	// Notify for Ctrl+C and other interrupt signals so the user can abort
	// the device authorization flow or other operations at any time.
	signals, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Initiate Device Authorization Flow to sign in to a Microsoft Account.
	da, err := MinecraftAndroid.DeviceAuth(signals)
	if err != nil {
		panic(fmt.Sprintf("error requesting device authorization flow: %s", err))
	}

	log.Printf(
		"Sign in to your Microsoft Account at %s using the code %s.",
		da.VerificationURI, da.UserCode,
	)

	// Poll for the access token while the user completes the browser sign-in.
	// The timeout is set to one minute. Increase if you need more time.
	pollCtx, cancel := context.WithTimeout(signals, time.Minute)
	defer cancel()
	token, err := MinecraftAndroid.DeviceAccessToken(pollCtx, da)
	if err != nil {
		panic(fmt.Sprintf("error polling access token in device authorization flow: %s", err))
	}
	// Use TokenSource so we can always use a valid token in fresh state.
	msa := MinecraftAndroid.TokenSource(context.Background(), token)

	// Make a SISU session using the Microsoft Account token source.
	session := MinecraftAndroid.New(msa, nil)

	// Request a XASU (Xbox Authentication Services for User) token using the SISU authorization endpoint.
	if _, err := session.UserToken(signals); err != nil {
		var acct *sisu.AccountRequiredError
		if errors.As(err, &acct) {
			log.Panicf("You need to create an Xbox Live account at: %s", acct.SignupURL)
		}
		panic(fmt.Sprintf("error requesting user token: %s", err))
	}

	// Log in to Xbox Live services using the SISU session.
	client, err := NewClient(session)
	if err != nil {
		panic(fmt.Sprintf("error creating API client: %s", err))
	}
	// Make sure to close the client when it's done.
	defer func() {
		if err := client.Close(); err != nil {
			panic(fmt.Sprintf("error closing API client: %s", err))
		}
	}()

	// Appear online in the social network.
	if err := client.Presence().Update(signals, presence.TitleRequest{
		State: presence.StateActive,
	}); err != nil {
		panic(fmt.Sprintf("error updating presence: %s", err))
	}

	// Use the social (peoplehub) endpoint to search a user using the query.
	ctx, cancel := context.WithTimeout(signals, time.Second*15)
	defer cancel()
	users, err := client.Social().Search(ctx, "Lactyy")
	if err != nil {
		panic(fmt.Sprintf("error searching for users: %s", err))
	}
	if len(users) == 0 {
		panic("no users found")
	}

	// Use the first user present in the result.
	user := users[0]
	fmt.Println(user.GamerTag)
}

// MinecraftAndroid is the SISU configuration for Minecraft: Bedrock Edition on Android.
//
// It provides all the parameters needed to authenticate a user with a Microsoft Account
// and authorize them for Xbox Live services using the SISU endpoints.
//
// Note that ClientID, RedirectURI, and other title-specific fields are fixed values tied
// to the Minecraft title. Do not modify them unless you are targeting a different title.
var MinecraftAndroid = sisu.Config{
	Config: xal.Config{
		// This indicates the device is running Android 13.
		Device: xal.Device{
			Type:    xal.DeviceTypeAndroid,
			Version: "13",
		},
		UserAgent: "XAL Android 2025.04.20250326.000",
		TitleID:   1739947436,
		Sandbox:   "RETAIL", // Usually 'RETAIL' for most games available in the market
	},

	// Those fields are title-specific and cannot be easily changed.
	// Treat them like constants that are specific to the title being authenticated for the user.
	ClientID:    "0000000048183522",               // Client ID used for authenticating and authorizing with Xbox Live
	RedirectURI: "ms-xal-0000000048183522://auth", // Used for Authorization Code Flow
}
