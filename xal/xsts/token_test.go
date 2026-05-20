package xsts

import (
	"testing"

	"github.com/df-mc/go-xsapi/v2/xal/xasu"
)

func TestTokenStringReturnsEmptyForIncompleteAuthValue(t *testing.T) {
	tests := []struct {
		name  string
		token *Token
	}{
		{
			name: "missing token",
			token: &Token{
				DisplayClaims: DisplayClaims{UserInfo: []UserInfo{{UserInfo: xasu.UserInfo{UserHash: "uhs"}}}},
			},
		},
		{
			name: "missing user hash",
			token: &Token{
				Token:         "token",
				DisplayClaims: DisplayClaims{UserInfo: []UserInfo{{}}},
			},
		},
		{
			name: "missing user claims",
			token: &Token{
				Token: "token",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.String(); got != "" {
				t.Fatalf("Token.String() = %q, want empty string", got)
			}
		})
	}
}

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
