package rta

import "errors"

// ErrUnavailable is returned when an operation requires an RTA connection, but
// no RTA connection is configured.
var ErrUnavailable = errors.New("rta: connection unavailable")
