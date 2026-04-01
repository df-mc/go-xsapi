package xal

import (
	"time"

	"github.com/df-mc/go-xsapi/xal/internal/timestamp"
)

// ServerTime returns the best known approximation of Microsoft's server time.
// Signed Xbox requests should prefer this value over [time.Now] so any clock
// skew learned during authentication is reused consistently.
func ServerTime() time.Time {
	return timestamp.Now()
}
