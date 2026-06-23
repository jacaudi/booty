package http

import (
	"log/slog"
	"net/http"
)

// writeError logs the underlying cause via slog (full internal detail stays
// off the wire) and writes status + a short plaintext reason. Boot/Ignition/
// Talos clients act on the status code, not on a structured error body, so
// plaintext + status is the right shape.
func writeError(w http.ResponseWriter, status int, msg string, err error) {
	if err != nil {
		slog.Error(msg, "status", status, "err", err)
	} else {
		slog.Error(msg, "status", status)
	}
	http.Error(w, msg, status)
}
