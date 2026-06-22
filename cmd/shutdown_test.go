package main

import (
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"
)

func TestShutdownRunsAllStepsInOrder(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var calls []string
	steps := []shutdownStep{
		{name: "http", stop: func() { calls = append(calls, "http") }},
		{name: "schedulers", stop: func() { calls = append(calls, "schedulers") }},
		{name: "tftp", stop: func() { calls = append(calls, "tftp") }},
	}

	shutdown(logger, steps)

	want := []string{"http", "schedulers", "tftp"}
	if !slices.Equal(calls, want) {
		t.Fatalf("shutdown ran steps in %v, want %v", calls, want)
	}
}

func TestShutdownSkipsNilStops(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var calls []string
	steps := []shutdownStep{
		{name: "http", stop: func() { calls = append(calls, "http") }},
		{name: "ostree-never-started", stop: nil},
		{name: "tftp", stop: func() { calls = append(calls, "tftp") }},
	}

	shutdown(logger, steps)

	want := []string{"http", "tftp"}
	if !slices.Equal(calls, want) {
		t.Fatalf("shutdown ran steps in %v, want %v", calls, want)
	}
}

func TestStopWithTimeoutReturnsOnCompletion(t *testing.T) {
	done := stopWithTimeout(func() {}, time.Second)
	if !done {
		t.Fatalf("stopWithTimeout: done = false, want true for a stop that completes")
	}
}

func TestStopWithTimeoutReturnsWhenStopBlocks(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	start := time.Now()
	done := stopWithTimeout(func() { <-block }, 20*time.Millisecond)
	elapsed := time.Since(start)

	if done {
		t.Fatalf("stopWithTimeout: done = true, want false when stop blocks past the bound")
	}
	if elapsed >= 200*time.Millisecond {
		t.Errorf("stopWithTimeout blocked for %v, want it to return near the 20ms bound", elapsed)
	}
}
