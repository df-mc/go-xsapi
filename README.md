# go-xsapi

[![Go Reference](https://pkg.go.dev/badge/github.com/df-mc/go-xsapi.svg)](https://pkg.go.dev/github.com/df-mc/go-xsapi)

>A Go library for communicating with Xbox Live API.

![Azure_Bit_Gopher.png](https://github.com/ashleymcnamara/gophers/blob/2951dcaac888f5489f762c959b1e1c31af48e92d/Azure_Bit_Gopher.png?raw=true)

## Example

This code demonstrates Device Authorization Code Flow to retrieve access token, and interacts with some of the API endpoints available in Xbox Live.

```go
// Notify for Ctrl+C and other interrupt signals so the user can abort
// the device authorization and other operations using contexts any time.
signals, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
defer cancel()

// We're using Device Authorization Flow here to log in to the Microsoft Account.
ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
defer cancel()
da, err := MinecraftAndroid.DeviceAuth(ctx)
if err != nil {
  panic(fmt.Sprintf("error requesting device authorization flow: %s", err))
}

_, _ = fmt.Fprintf(os.Stderr, "Sign in to your Microsoft Account at %s using the code %s.", da.VerificationURI, da.UserCode)

// Make a context for polling access token while user is signing in to their Microsoft Account
// using the displayed user code. We give one minute to complete login. You may also specify longer timeout.
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
ctx, cancel = context.WithTimeout(signals, time.Second*15)
defer cancel()
users, err := client.Social().Search(ctx, "Username")
if err != nil {
  panic(fmt.Sprintf("error searching for users: %s", err))
}
if len(users) == 0 {
  panic("no users found")
}

// Use the first user present in the result.
user := users[0]
log.Print(user.GamerTag)
```

## Contact

[![Discord Banner 2](https://discordapp.com/api/guilds/623638955262345216/widget.png?style=banner2)](https://discord.gg/U4kFWHhTNR)