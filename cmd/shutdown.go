package main

import "log/slog"

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
