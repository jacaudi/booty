package http

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func servingStore(t *testing.T) *db.Store {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	dir := t.TempDir()
	viper.Set(config.DataDir, dir)
	viper.Set(config.ServerIP, "10.0.0.1")
	viper.Set(config.ServerHttpPort, "8080")
	s, err := db.Open(filepath.Join(dir, "booty.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	hardware.SetStore(s)
	t.Cleanup(func() { hardware.SetStore(nil); s.Close() })
	return s
}

// writeFile writes <dataDir>/<rel> with content.
func writeFile(t *testing.T, rel, content string) {
	t.Helper()
	p := filepath.Join(viper.GetString(config.DataDir), rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestIgnitionUnboundHostByteIdenticalServerIPPort is the acceptance-criterion #4
// regression guard: an UNBOUND known host falls through to the file path, and its
// ignition artifact URLs still carry host:port via .ServerIP (per-family var
// population must not have collapsed .ServerIP to host-only).
func TestIgnitionUnboundHostByteIdenticalServerIPPort(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.IgnitionFile, "config/ignition.yaml")
	writeFile(t, "config/ignition.yaml",
		"variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: /etc/x\n      contents:\n        source: http://{{ .ServerIP }}/data/config/x.sh\n")
	const mac = "aa:bb:cc:dd:ee:30"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar", Approved: true}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ignition.json?mac="+mac, nil)
	handleIgnitionRequest(s)(rr, req)

	if rr.Code != 200 {
		t.Fatalf("ignition = %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "http://10.0.0.1:8080/data/config/x.sh") {
		t.Fatalf("unbound ignition must keep host:port URL, got: %s", rr.Body.String())
	}
}

// TestIgnitionBoundConfigServed: a host bound to a DB butane config serves the
// translated DB source, not the file.
func TestIgnitionBoundConfigServed(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.IgnitionFile, "config/ignition.yaml")
	writeFile(t, "config/ignition.yaml", "variant: fcos\nversion: 1.5.0\n") // file path, unused here
	const mac = "aa:bb:cc:dd:ee:31"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar", Approved: true}); err != nil {
		t.Fatal(err)
	}
	cid, _ := s.CreateConfig("bound", "butane")
	rid, _, _ := s.AddConfigRevision(cid,
		base64.StdEncoding.EncodeToString([]byte("variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: /etc/bound\n      contents:\n        source: http://{{ .ServerIP }}/bound\n")), "sha")
	s.SetActiveRevision(cid, rid)
	if err := hardware.SetHostConfig(mac, &cid); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ignition.json?mac="+mac, nil)
	handleIgnitionRequest(s)(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "http://10.0.0.1:8080/bound") {
		t.Fatalf("bound config not served: %d %s", rr.Code, rr.Body.String())
	}
}

// TestPreseedServesServerDefaultFile: an unbound debian host gets the
// --preseedFile server default (the new serving rung-4).
func TestPreseedServesServerDefaultFile(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.PreseedFile, "config/preseed.cfg")
	writeFile(t, "config/preseed.cfg", "d-i mirror/host string {{ .ServerIP }}")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/preseed", nil)
	handlePreseedRequest(s)(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "10.0.0.1") {
		t.Fatalf("preseed default file: %d %s", rr.Code, rr.Body.String())
	}
}
