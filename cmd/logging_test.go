package main

import (
	"log/slog"
	"testing"
)

func TestLogLevelHonorsDebugFlag(t *testing.T) {
	if got := logLevel(true); got != slog.LevelDebug {
		t.Errorf("logLevel(true) = %v, want %v", got, slog.LevelDebug)
	}
	if got := logLevel(false); got != slog.LevelInfo {
		t.Errorf("logLevel(false) = %v, want %v", got, slog.LevelInfo)
	}
}
