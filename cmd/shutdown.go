package main

import (
	"log/slog"
	"time"
)

// shutdownStep is one named subsystem to stop during graceful shutdown.
// A nil stop is skipped (e.g. a subsystem that never finished starting).
type shutdownStep struct {
	name string
	stop func()
}

// shutdown runs each step's stop in the given order, logging around each.
// Steps with a nil stop are skipped. Each stop is expected to block until its
// subsystem has drained; callers order steps so the most front-facing
// subsystem (HTTP) drains before background work is stopped.
func shutdown(logger *slog.Logger, steps []shutdownStep) {
	for _, step := range steps {
		if step.stop == nil {
			continue
		}
		logger.Info("stopping subsystem", "subsystem", step.name)
		step.stop()
		logger.Info("subsystem stopped", "subsystem", step.name)
	}
}

// stopWithTimeout runs stop in a goroutine and waits up to d for it to return.
// It reports whether stop completed within the bound (true) or the bound was
// hit first (false). The bound remains as a safety net; pin/tftp v3's
// single-port Shutdown returns promptly and Server.Shutdown closes the socket itself.
func stopWithTimeout(stop func(), d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
