package nsal

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"time"
)

// SignaturePolicy encapsulates a policy for signing requests in NSAL.
type SignaturePolicy struct {
	// Version is the version for the SignaturePolicy. It is typically 0
	// for most SignaturePolicy listed in a TitleData.
	Version uint32
	// SupportedAlgorithms is a list of algorithms supported for signing
	// the request. The meanings or semantics of this field is currently
	// unknown. Known values are 'XBL' and 'DPoP'.
	SupportedAlgorithms []string
	// MaxBodyBytes is the maximum length of request body that can be signed
	// in a single request when using this policy. If 0, the whole request
	// body will be signed.
	MaxBodyBytes int
	// ExtraHeaders specifies additional headers that should be signed.
	// It is typically empty, but rarely used in some titles.
	// Note that the 'Authorization' header of the request is always
	// signed.
	ExtraHeaders []string
}

// Sign signs the request and sets the 'Signature' header. The provided request
// body will be used for computing an SHA-256 hash. The ECDSA private key will
// be used to sign the request, which must be same from the ProofKey field of
// authentication requests. The timestamp is included in the signature data
// and must be close to the server time as possible.
func (policy SignaturePolicy) Sign(request *http.Request, body []byte, key *ecdsa.PrivateKey, timestamp time.Time) {
	currentTime := windowsTimestamp(timestamp)
	hash := sha256.New()

	// Signature policy version (0, 0, 0, 1) + 0 byte.
	buf := &bytes.Buffer{}
	_ = binary.Write(buf, binary.BigEndian, policy.Version)
	buf.WriteByte(0)
	// Timestamp + 0 byte.
	_ = binary.Write(buf, binary.BigEndian, currentTime)
	buf.WriteByte(0)
	hash.Write(buf.Bytes())

	// HTTP method, generally POST + 0 byte.
	hash.Write([]byte(request.Method))
	hash.Write([]byte{0})
	// Request uri path + raw query + 0 byte.
	path := request.URL.Path
	if rq := request.URL.RawQuery; rq != "" {
		path += "?" + rq
	}
	hash.Write([]byte(path))
	hash.Write([]byte{0})

	// Authorization header and extra headers if present, otherwise an empty string + 0 byte.
	for _, header := range append([]string{"Authorization"}, policy.ExtraHeaders...) {
		hash.Write([]byte(request.Header.Get(header)))
		hash.Write([]byte{0})
	}

	// Body data (only up to a certain limit, but this limit is practically never reached) + 0 byte.
	if policy.MaxBodyBytes == 0 {
		hash.Write(body)
	} else {
		// MaxBodyBytes is typically 8192 for most Xbox services since they all
		// share the same signature policy on the default title data.
		hash.Write(body[:min(policy.MaxBodyBytes, len(body))])
	}
	hash.Write([]byte{0})

	// Sign the checksum produced, and combine the 'r' and 's' into a single signature.
	// Encode r and s as 32-byte, zero-padded big-endian values so the P-256 signature is always exactly 64 bytes long.
	r, s, _ := ecdsa.Sign(rand.Reader, key, hash.Sum(nil))
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])

	// The signature begins with 12 bytes, the first being the signature policy version (0, 0, 0, 1) again,
	// and the other 8 the timestamp again.
	buf = bytes.NewBuffer(binary.BigEndian.AppendUint32(nil, policy.Version))
	_ = binary.Write(buf, binary.BigEndian, currentTime)

	// Append the signature to the other 12 bytes, and encode the signature with standard base64 encoding.
	sig := append(buf.Bytes(), signature...)
	request.Header.Set("Signature", base64.StdEncoding.EncodeToString(sig))
}

// AuthPolicy is the hardcoded signature policy used for authentication/authorization
// requests for Xbox Live, including XASD, XAST, XASU, and XSTS.
var AuthPolicy = SignaturePolicy{
	Version: 1,
}

// windowsTimestamp returns a Windows specific timestamp. It has a certain offset from Unix time
// which must be accounted for.
//
// See: https://learn.microsoft.com/en-us/windows/win32/sysinfo/file-times
func windowsTimestamp(t time.Time) int64 {
	return t.UnixNano()/100 + 116444736000000000
}
