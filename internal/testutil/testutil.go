package testutil

import (
	"io"
	"log/slog"
)

// SlogDiscard returns a logger that discards all output.
func SlogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
