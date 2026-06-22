package main

import (
	"log/slog"
	"os"
)

// logLevel maps the debug flag to a slog level: debug logging when the flag is
// set, info otherwise. The flag is the only knob; it drives the level so
// verbose call sites can emit slog.Debug unconditionally.
func logLevel(debug bool) slog.Level {
	if debug {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// setupLogging installs a single process-wide structured logger writing to
// stderr (matching the prior std log destination) at the level the debug flag
// selects.
func setupLogging(debug bool) {
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel(debug)})
	slog.SetDefault(slog.New(handler))
}
