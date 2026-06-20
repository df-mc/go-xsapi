package internal

import (
	"testing"
	"time"
)

func TestTokenValidRequiresExpirationPastSkew(t *testing.T) {
	tests := []struct {
		name     string
		token    *Token[struct{}]
		expected bool
	}{
		{
			name:     "missing token",
			token:    &Token[struct{}]{NotAfter: time.Now().Add(time.Hour)},
			expected: false,
		},
		{
			name:     "expires inside skew",
			token:    &Token[struct{}]{Token: "token", NotAfter: time.Now().Add(tokenExpirationSkew / 2)},
			expected: false,
		},
		{
			name:     "expires after skew",
			token:    &Token[struct{}]{Token: "token", NotAfter: time.Now().Add(tokenExpirationSkew * 2)},
			expected: true,
		},
		{
			name:     "nil token",
			token:    nil,
			expected: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.token.Valid(); got != tt.expected {
				t.Fatalf("Valid() = %t, want %t", got, tt.expected)
			}
		})
	}
}
