package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/viper"
)

func TestHandleHealthz(t *testing.T) {
	viper.Set("version", "v-test") // booty binds the ldflags build version to this viper key at startup (cmd/main.go:257-259)
	t.Cleanup(func() { viper.Set("version", "") })

	rr := httptest.NewRecorder()
	handleHealthz(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v (%q)", err, rr.Body.String())
	}
	if body["status"] != "ok" {
		t.Fatalf(`status = %q, want "ok"`, body["status"])
	}
	if body["version"] != "v-test" {
		t.Fatalf(`version = %q, want "v-test"`, body["version"])
	}
}
