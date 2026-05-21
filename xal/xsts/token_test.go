package xsts

import (
	"testing"

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
