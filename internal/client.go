package internal

type contextKey struct{}

var ETag contextKey

// XBLRelyingParty is the relying party used for various Xbox Live services.
// In XSAPI Client, it will be used for requesting NSAL endpoints for current
// authenticated title.
const XBLRelyingParty = "http://xboxlive.com"
