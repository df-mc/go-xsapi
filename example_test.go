package xsapi

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/df-mc/go-xsapi/xal"
	"github.com/df-mc/go-xsapi/xal/sisu"
)

func ExampleClient() {
	// Notify for Ctrl+C and other interrupt signals so the user can abort
	// the device authorization flow or other operations at any time.
	signals, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	// Use the Device Authorization Flow to sign in to a Microsoft Account.
	da, err := MinecraftAndroid.DeviceAuth(signals)
	if err != nil {
		panic(fmt.Sprintf("error requesting device authorization flow: %s", err))
	}

	// We print out the verification URI and the user code to [os.Stderr]
	// so it doesn't need to be captured by Output: line in this example.
	_, _ = fmt.Fprintf(os.Stderr,
		"Sign in to your Microsoft Account at %s using the code %s.",
		da.VerificationURI, da.UserCode,
	)

	// Make a context for polling the access token while the user completes sign-in.
	// In this case, we allow one minute to complete login (you may configure a longer timeout).
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

	// Log in to Xbox Live services using the SISU session.
	client, err := NewClient(session, nil)
	if err != nil {
		panic(fmt.Sprintf("error creating API client: %s", err))
	}
	// Make sure to close the client when it's done.
	defer func() {
		if err := client.Close(); err != nil {
			panic(fmt.Sprintf("error closing API client: %s", err))
		}
	}()

	// Use social (peoplehub) endpoint to search a user using the query.
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
	// Output: Lactyy
}

// MinecraftAndroid is the SISU configuration used in Minecraft: Bedrock Edition for Android.
// It is used for authenticating and authorizing with Xbox Live services.
var MinecraftAndroid = sisu.Config{
	Config: xal.Config{
		// This indicates the device is running Android 13.
		Device: xal.Device{
			Type:    xal.DeviceTypeAndroid,
			Version: "13",
		},
		UserAgent: "XAL Android 2025.04.20250326.000",
		TitleID:   1739947436,
	},

	// Those fields are title-specific and cannot be easily changed.
	// Treat them like constants that are specific to the title being authenticated for the user.
	ClientID:    "0000000048183522",               // Client ID used for authenticating and authorizing with Xbox Live
	RedirectURI: "ms-xal-0000000048183522://auth", // Used for Authorization Code Flow
	Sandbox:     "RETAIL",                         // Usually 'RETAIL' for most games available in the market
}
