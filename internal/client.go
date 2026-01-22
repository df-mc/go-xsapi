package internal

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/df-mc/go-xsapi/xal/nsal"
	"github.com/df-mc/go-xsapi/xal/xsts"
)

type HTTPClient interface {
	HTTPClient() *http.Client
}

type TokenAndSignature interface {
	TokenAndSignature(ctx context.Context, u *url.URL) (token *xsts.Token, policy nsal.SignaturePolicy, err error)
}

type Logger interface {
	Log() *slog.Logger
}

type contextKey struct{}

var ETag contextKey

// XBLRelyingParty is the relying party used for various Xbox Live services.
// In XSAPI Client, it will be used for requesting NSAL endpoints for current
// authenticated title.
const XBLRelyingParty = "http://xboxlive.com"
