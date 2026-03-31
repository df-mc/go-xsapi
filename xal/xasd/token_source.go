package xasd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"sync"

	"github.com/df-mc/go-xsapi/xal"
)

// TokenSource is the interface that supplies device tokens and the proof key
// used to sign requests.
type TokenSource interface {
	// DeviceToken returns a device token, requesting a new one if necessary.
	DeviceToken(ctx context.Context) (*Token, error)
	// ProofKey returns the [ecdsa.PrivateKey] used to sign requests.
	ProofKey() *ecdsa.PrivateKey
}

// ReuseTokenSource returns a [TokenSource] that caches and automatically
// refreshes device tokens as they expire.
//
// t and proofKey may be restored from a previous session to avoid
// re-authenticating. They must always be stored and restored together,
// as the proof key is bound to the device token and is required to sign
// requests on its behalf.
//
// If proofKey is nil, a new one is generated. In that case, t must also
// be nil, as an existing device token cannot be used with a different proof key.
func ReuseTokenSource(config xal.Config, t *Token, proofKey *ecdsa.PrivateKey) TokenSource {
	if proofKey == nil {
		if t != nil {
			panic("xal/xasd: reusing a device token requires proof key")
		}
		var err error
		proofKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(fmt.Sprintf("xal/xasd: generate proof key: %s", err))
		}
	}
	return &refreshTokenSource{
		config:   config,
		t:        t,
		proofKey: proofKey,
	}
}

// refreshTokenSource is a [TokenSource] that caches a device token and
// refreshes it automatically when it expires.
type refreshTokenSource struct {
	config xal.Config

	t        *Token
	mu       sync.Mutex
	proofKey *ecdsa.PrivateKey
}

// DeviceToken returns the cached device token if still valid, or requests
// a new one via [Authenticate].
func (r *refreshTokenSource) DeviceToken(ctx context.Context) (*Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.t != nil && r.t.Valid() {
		return r.t, nil
	}
	t, err := Authenticate(ctx, r.config, r.proofKey)
	if err != nil {
		return nil, fmt.Errorf("authenticate: %w", err)
	}
	r.t = t
	return r.t, nil
}

// ProofKey returns the proof key associated with this token source.
func (r *refreshTokenSource) ProofKey() *ecdsa.PrivateKey {
	return r.proofKey
}
