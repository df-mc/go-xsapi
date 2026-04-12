package sisu

import (
	"github.com/df-mc/go-xsapi/v2/xal"
)

// Config represents a configuration for a title using SISU for authenticating or
// authorizing users in Xbox Live.
type Config struct {
	// An embedded [xal.Config] specifies additional parameters for requesting
	// device tokens.
	xal.Config

	// ClientID is the OAuth2 application ID used to authenticate the user with
	// a Microsoft account. It is also used to authorize with various Xbox Live
	// authentication services.
	ClientID string

	// RedirectURI is the URI defined by the title to be used as the redirect URL
	// of the OAuth2 WebView authentication flow. Callers should listen or handle
	// redirections in the webview and match the URI with this URI.
	// RedirectURI may start with 'ms-xal-<number>://', which requires the caller
	// to handle redirections in the WebView displayed to sign in to the user.
	RedirectURI string
}
