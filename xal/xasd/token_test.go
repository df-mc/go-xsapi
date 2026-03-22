//go:build network

package xasd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/xal"
)

// TestAndroid simulates an Android phone on the XASD.
func TestAndroid(t *testing.T) {
	testConfig(t, xal.Config{
		Device: xal.Device{
			Type:    xal.DeviceTypeAndroid,
			Version: "15",
		},
		UserAgent: "XAL Android 2025.04.20250326.000",
	})
}

// TestIOS simulates an iPhone or iPad device running iOS on the XASD.
func TestIOS(t *testing.T) {
	testConfig(t, xal.Config{
		Device: xal.Device{
			Type:    xal.DeviceTypeIOS,
			Version: "26.2",
		},
		UserAgent: "XAL iOS 2021.11.20211021.000",
	})
}

// TestNintendo simulates a Nintendo Switch console on the XASD.
func TestNintendo(t *testing.T) {
	testConfig(t, xal.Config{
		Device: xal.Device{
			Type:    xal.DeviceTypeNintendo,
			Version: "21.1.0",
		},
		UserAgent: "XAL",
	})
}

// TestPlayStation simulates a PlayStation console on the XASD.
func TestPlayStation(t *testing.T) {
	testConfig(t, xal.Config{
		Device: xal.Device{
			Type:    xal.DeviceTypePlayStation,
			Version: "25.08",
		},
		UserAgent: "XAL",
	})
}

// TestWin32 simulates a Win32 device on the XASD.
func TestWin32(t *testing.T) {
	testConfig(t, xal.Config{
		Device: xal.Device{
			Type:    xal.DeviceTypeWin32,
			Version: "10.0.26200",
		},
		UserAgent: "XAL GRTS 2025.10.20251022.000",
	})
}

// testConfig authenticates a device against the XASD using the provided
// configuration and verifies that a valid device token is returned.
//
// It is intended for reuse by platform-specific tests and is commonly used
// to validate values defined by the xal.DeviceType* constants.
func testConfig(t testing.TB, config xal.Config) {
	proofKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("error generating proof key: %s", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second*15)
	defer cancel()
	token, err := Authenticate(ctx, config, proofKey)
	if err != nil {
		t.Fatalf("error authenticating device: %s", err)
	}
	if !token.Valid() {
		t.Fatal("device token is not valid")
	}
}
