package http

import (
	"log/slog"
	"net/http"
)

// msgDestructiveNotImplemented is the single wording for the SEVEN DELETE/PUT
// operations that are WIRED BUT NOT IMPLEMENTED. Its predecessor said
// "disabled until authentication lands (P10)", which became false the moment
// auth landed: these endpoints have no implementation behind the guard, so
// authenticating changes nothing about them. One literal, one knowledge site.
//
// delete-host is NOT among them any more — it is implemented (api_hosts.go).
const msgDestructiveNotImplemented = "this destructive endpoint is not implemented yet"

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

// writeBody writes data as the response body. A failed write means the client
// went away and there is nobody to tell, so it is logged at debug, not returned.
func writeBody(w http.ResponseWriter, data []byte) {
	//nolint:gosec // G705: callers write Ignition JSON, iPXE and preseed text or YAML rendered from operator templates, never HTML
	if _, err := w.Write(data); err != nil {
		slog.Debug("writing response body failed", "err", err)
	}
}
