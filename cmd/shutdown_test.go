package main

import (
	"io"
	"log/slog"
	"slices"
	"testing"
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
