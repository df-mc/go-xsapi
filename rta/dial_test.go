package rta

import (
	"testing"
	"time"
)

// TestReconnectBackoff checks the jitter bounds, cap, and independent schedules
// used by the dial and interrupted-resubscribe retry loops.
func TestReconnectBackoff(t *testing.T) {
	first, second := newReconnectBackoff(), newReconnectBackoff()
	for attempt, base := range []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, time.Minute, time.Minute,
	} {
		if delay := first.NextBackOff(); delay < base || delay > base+base/2 {
			t.Fatalf("attempt %d: delay = %s, want [%s, %s]", attempt, delay, base, base+base/2)
		}
	}
	if delay := second.NextBackOff(); delay < time.Second || delay > 1500*time.Millisecond {
		t.Fatalf("second schedule starts at %s, want [1s, 1.5s]", delay)
	}
}
