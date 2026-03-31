package timestamp

import (
	"net/http"
	"sync"
	"time"
)

var (
	// serverTimeDelta is the offset to add to time.Now() to approximate Microsoft's server time, based on the
	// most recent Date header we received.
	//
	// Signed Xbox Live requests can be rejected if the client timestamp is too far from server time.
	serverTimeDelta time.Duration
	// serverTimeMu guards serverTimeDelta from concurrent read/write access.
	serverTimeMu sync.RWMutex
)

// Update computes the delta between the local clock and Microsoft's server time
// using the 'Date' header of an HTTP response, and caches it for later use by [Now].
// If the header is absent or cannot be parsed, the cached delta is left unchanged.
func Update(header http.Header) {
	date := header.Get("Date")
	if date == "" {
		return
	}
	t, err := time.Parse(time.RFC1123, date)
	if err != nil || t.IsZero() {
		return
	}
	serverTimeMu.Lock()
	serverTimeDelta = time.Until(t)
	serverTimeMu.Unlock()
}

// Now returns a [time.Time] that closely approximates Microsoft's server time,
// suitable for inclusion in a signed Xbox Live request.
//
// If [Update] has been called with a valid 'Date' response header, the cached
// delta is applied to the current local time to compensate for clock skew.
// Signed requests are rejected if the timestamp in the signature differs too
// much from the server time.
func Now() time.Time {
	serverTimeMu.RLock()
	delta := serverTimeDelta
	serverTimeMu.RUnlock()

	t := time.Now()
	if delta != 0 {
		t = t.Add(delta)
	}
	return t
}
