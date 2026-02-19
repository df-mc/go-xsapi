package internal

import (
	"context"
	"crypto/tls"
	"net/http"
)

// contextKey is an unexported type for context key used in HTTPClient.
type contextKey struct{}

// HTTPClient is the context key used for specifying an [http.Client] in a [context.Context]
// passed to the API call.
var HTTPClient contextKey

// ContextClient returns an [http.Client] from the [context.Context] if possible,
// otherwise it returns [http.DefaultClient].
func ContextClient(ctx context.Context) *http.Client {
	if value, ok := ctx.Value(HTTPClient).(*http.Client); ok {
		return value
	}
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Renegotiation: tls.RenegotiateOnceAsClient,
			},
		},
	}
}
