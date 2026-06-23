package xsts

import (
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/xal/xasu"
)

func TestTokenStringFormatsCompleteAuthValue(t *testing.T) {
	token := &Token{
		Token: "token",
		DisplayClaims: DisplayClaims{UserInfo: []UserInfo{{
			UserInfo: xasu.UserInfo{UserHash: "uhs"},
		}}},
	}

	if got, want := token.String(), "XBL3.0 x=uhs;token"; got != want {
		t.Fatalf("Token.String() = %q, want %q", got, want)
	}
}

func TestTokenValidRequiresExpirationPastSkew(t *testing.T) {
	token := &Token{
		Token:    "token",
		NotAfter: time.Now().Add(30 * time.Second),
		DisplayClaims: DisplayClaims{UserInfo: []UserInfo{{
			UserInfo: xasu.UserInfo{UserHash: "uhs"},
		}}},
	}
	if token.Valid() {
		t.Fatal("Valid() = true, want false for token expiring inside skew")
	}

	token.NotAfter = time.Now().Add(2 * time.Minute)
	if !token.Valid() {
		t.Fatal("Valid() = false, want true")
	}
}
