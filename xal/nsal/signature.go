package nsal

import (
	"github.com/df-mc/go-xsapi/v2/xal/internal"
)

// SignaturePolicy encapsulates a policy for signing requests in NSAL.
type SignaturePolicy = internal.SignaturePolicy

// AuthPolicy is the hardcoded signature policy used for authentication/authorization
// requests for Xbox Live, including XASD, XAST, XASU, and XSTS.
var AuthPolicy = internal.AuthPolicy
