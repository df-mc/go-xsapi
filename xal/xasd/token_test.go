package xasd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"

	"github.com/df-mc/go-xsapi/xal"
)

func TestPlayStation(t *testing.T) {
	proofKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("error generating proof key: %s", err)
	}
	tok, err := Authenticate(t.Context(), xal.Config{
		UserAgent: "XAL",
		Device: xal.Device{
			Type:    xal.DeviceTypePlayStation,
			Version: "10.0.0",
		},
	}, proofKey)
	if err != nil {
		t.Fatalf("error authenticating device: %s", err)
	}
	t.Logf("%#v", tok)
}
