package sisu

import (
	"context"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/microsoft"
)

// DeviceAuth returns a device auth struct which contains a device code
// and authorization information provided for users to enter on another device.
func (conf Config) DeviceAuth(ctx context.Context) (*oauth2.DeviceAuthResponse, error) {
	return conf.oauth2().DeviceAuth(ctx, oauth2.SetAuthURLParam("response_type", "device_code"))
}

// DeviceAccessToken continuously polls the access token in the device authentication flow.
// If the [context.Context] has exceeded its deadline, it will return a nil *oauth2.Token with
// the contextual error.
func (conf Config) DeviceAccessToken(ctx context.Context, da *oauth2.DeviceAuthResponse) (*oauth2.Token, error) {
	return conf.oauth2().DeviceAccessToken(ctx, da)
}

// oauth2 returns an [oauth2.Config] that may be used for exchanging access tokens
// or starting a device code authentication flow using Windows Live tokens.
func (conf Config) oauth2() *oauth2.Config {
	endpoint := microsoft.LiveConnectEndpoint
	endpoint.DeviceAuthURL = "https://login.live.com/oauth20_connect.srf"

	return &oauth2.Config{
		Endpoint: endpoint,
		ClientID: conf.ClientID,
		Scopes:   []string{"service::user.auth.xboxlive.com::MBI_SSL"},
	}
}
