package http

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestDataFileHandler_SymlinkResolution: /data/ judges where a request RESOLVES,
// not just what was requested. [os.Root] follows a relative symlink that stays
// inside dataDir, so a link planted in public/ (or in an allowlisted ancestor
// directory) must not reach api-token, config/ or an in-flight download.
// Links whose targets are themselves allowlisted keep working.
func TestDataFileHandler_SymlinkResolution(t *testing.T) {
	dataDir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dataDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("api-token", "SECRET-TOKEN")
	write("config/ignition.yaml", "SECRET-CONFIG")
	write("public/ok.txt", "ok")
	write("public/other.txt", "other")
	write("cache/talos/kernel", "kernel")
	write("cache/talos/kernel.partial", "SECRET-PARTIAL")
	for link, target := range map[string]string{
		"public/tok":       "../api-token",
		"public/up":        "..",
		"public/cfgdir":    "../config",
		"public/hop1":      "hop2",
		"public/hop2":      "../api-token",
		"public/loop1":     "loop2",
		"public/loop2":     "loop1",
		"public/inflight":  "../cache/talos/kernel.partial",
		"public/samelevel": "other.txt",
		"public/tocache":   "../cache/talos/kernel",
		"cache/back":       "../public/other.txt",
	} {
		if err := os.Symlink(target, filepath.Join(dataDir, link)); err != nil {
			t.Fatal(err)
		}
	}
	mux := dataMux(t, dataDir)

	cases := []struct {
		target string
		want   string // body; "" means must be 404
	}{
		// Refused: resolves to a non-allowlisted file.
		{"/data/public/tok", ""},
		{"/data/public/up/api-token", ""},
		{"/data/public/cfgdir/ignition.yaml", ""},
		{"/data/public/hop1", ""},
		{"/data/public/loop1", ""},
		{"/data/public/inflight", ""},
		// Served: resolves to an allowlisted file.
		{"/data/public/samelevel", "other"},
		{"/data/public/tocache", "kernel"},
		{"/data/public/up/public/ok.txt", "ok"},
		{"/data/cache/back", "other"},
		{"/data/cache/talos/kernel", "kernel"},
		{"/data/public/ok.txt", "ok"},
	}
	for _, c := range cases {
		code, body, _ := getData(mux, c.target)
		if c.want == "" {
			if code != http.StatusNotFound || bytes.Contains(body, []byte("SECRET")) {
				t.Errorf("%s = %d %q, want 404 with no secret", c.target, code, body)
			}
			continue
		}
		if code != http.StatusOK || string(body) != c.want {
			t.Errorf("%s = %d %q, want 200 %q", c.target, code, body, c.want)
		}
	}
}
