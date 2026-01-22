package sisu

import (
	"github.com/df-mc/go-xsapi/xal"
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
	RedirectURI string

	// Sandbox is the sandbox used to authenticate or authorize a session in Xbox Live.
	// It is "RETAIL" for most retail games available in Xbox Live.
	Sandbox string
}
