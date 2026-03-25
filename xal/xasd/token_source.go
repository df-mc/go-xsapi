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

type TokenSource interface {
	DeviceToken(ctx context.Context) (*Token, error)
	ProofKey() *ecdsa.PrivateKey
}

func ReuseTokenSource(config xal.Config, t *Token, proofKey *ecdsa.PrivateKey) TokenSource {
	if proofKey == nil {
		if t == nil {
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

type refreshTokenSource struct {
	config xal.Config

	t        *Token
	mu       sync.Mutex
	proofKey *ecdsa.PrivateKey
}

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

func (r *refreshTokenSource) Snapshot() *Token {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.t
}

func (r *refreshTokenSource) ProofKey() *ecdsa.PrivateKey {
	return r.proofKey
}
