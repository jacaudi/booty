package http

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteError(t *testing.T) {
	var logBuf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })

	rec := httptest.NewRecorder()
	writeError(rec, http.StatusInternalServerError, "render failed", errors.New("boom: secret detail"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "render failed") {
		t.Errorf("body = %q, want short reason", body)
	}
	if strings.Contains(body, "secret detail") {
		t.Errorf("internal error detail leaked to wire: %q", body)
	}
	if !strings.Contains(logBuf.String(), "secret detail") {
		t.Errorf("cause not logged: %q", logBuf.String())
	}
}
