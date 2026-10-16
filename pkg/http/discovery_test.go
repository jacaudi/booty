package http

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
)

func noARPFn(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no arp") }

// getBoot serves one request to h and returns the recorder. remote overrides
// the request's RemoteAddr when non-empty.
func getBoot(t *testing.T, h http.Handler, method, target, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody)
	if remote != "" {
		req.RemoteAddr = remote
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func countDiscovered(t *testing.T, s *db.Store) int {
	t.Helper()
	n, err := s.CountDiscoveredHosts(t.Context())
	if err != nil {
		t.Fatalf("CountDiscoveredHosts: %v", err)
	}
	return n
}

func TestDiscoveredArch(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"x86_64", "amd64"},
		{"i386", "amd64"},
		{"arm64", "arm64"},
		{"arm32", ""},
		{"sparc", ""},
		{"", ""},
		{"AMD64", ""},
	} {
		if got := discoveredArch(tc.raw); got != tc.want {
			t.Errorf("discoveredArch(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestDiscoverySource(t *testing.T) {
	for _, tc := range []struct{ remote, wantKey, wantIP string }{
		{"10.0.0.5:1234", "10.0.0.5", "10.0.0.5"},
		{"[::ffff:10.0.0.5]:1", "10.0.0.5", "10.0.0.5"},
		{"[2001:db8:1:2:aaaa::1]:1", "2001:db8:1:2::/64", "2001:db8:1:2:aaaa::1"},
		{"[fe80::1%eth0]:1", "fe80::/64", "fe80::1"},
		{"garbage", "garbage", "garbage"},
	} {
		key, ip := discoverySource(tc.remote)
		if key != tc.wantKey || ip != tc.wantIP {
			t.Errorf("discoverySource(%q) = (%q, %q), want (%q, %q)", tc.remote, key, ip, tc.wantKey, tc.wantIP)
		}
	}
}

func TestBootScriptRecordsUnknownMAC(t *testing.T) {
	s := servingStore(t)
	h := bootScriptHandler(s, noARPFn)
	rr := getBoot(t, h, http.MethodGet, "/booty.ipxe?mac=aa:bb:cc:dd:ee:b0&arch=x86_64", "10.0.0.7:5")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "not yet approved") {
		t.Fatalf("status %d body %q, want 200 holding script", rr.Code, rr.Body.String())
	}
	rows, err := s.ListDiscoveredHosts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one", rows)
	}
	if got := rows[0]; got.MAC != "aa:bb:cc:dd:ee:b0" || got.Arch != "amd64" || got.IP != "10.0.0.7" {
		t.Errorf("row = %+v, want MAC aa:bb:cc:dd:ee:b0 Arch amd64 IP 10.0.0.7", got)
	}
}

func TestBootScriptBodyUnchangedByRecording(t *testing.T) {
	s := servingStore(t)
	const target = "/booty.ipxe?mac=aa:bb:cc:dd:ee:b3&arch=arm64"
	with := getBoot(t, bootScriptHandler(s, noARPFn), http.MethodGet, target, "10.0.0.7:5")
	without := getBoot(t, bootScriptHandler(nil, noARPFn), http.MethodGet, target, "10.0.0.7:5")
	if with.Body.String() != without.Body.String() {
		t.Errorf("recording changed the body:\nwith:    %q\nwithout: %q", with.Body.String(), without.Body.String())
	}
}

func TestBootScriptRepeatIsDebounced(t *testing.T) {
	s := servingStore(t)
	h := bootScriptHandler(s, noARPFn)
	const target = "/booty.ipxe?mac=aa:bb:cc:dd:ee:b4"
	getBoot(t, h, http.MethodGet, target, "10.0.0.7:5")
	getBoot(t, h, http.MethodGet, target, "10.0.0.8:5")
	rows, err := s.ListDiscoveredHosts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].IP != "10.0.0.7" {
		t.Errorf("rows = %+v, want one row still at IP 10.0.0.7", rows)
	}
}

func TestBootScriptLimitAppliesOnlyToNewRows(t *testing.T) {
	s := servingStore(t)
	h := bootScriptHandler(s, noARPFn)
	for range 20 {
		getBoot(t, h, http.MethodGet, "/booty.ipxe?mac=02:00:00:00:01:00", "10.0.0.20:1")
	}
	for i := 1; i <= 7; i++ {
		getBoot(t, h, http.MethodGet, fmt.Sprintf("/booty.ipxe?mac=02:00:00:00:01:%02x", i), "10.0.0.20:1")
	}
	if got := countDiscovered(t, s); got != 8 {
		t.Errorf("count = %d, want 8: the 20 refreshes must use no budget", got)
	}
}

func TestBootScriptRateLimitsNewRowsPerIP(t *testing.T) {
	s := servingStore(t)
	h := bootScriptHandler(s, noARPFn)
	newMACs := func(prefix byte, n int, remote string) {
		t.Helper()
		for i := range n {
			getBoot(t, h, http.MethodGet, fmt.Sprintf("/booty.ipxe?mac=02:00:00:00:%02x:%02x", prefix, i), remote)
		}
	}
	steps := []struct {
		prefix byte
		n      int
		remote string
		want   int
	}{
		{0x10, 9, "10.0.0.30:1", 8},
		{0x11, 1, "10.0.0.31:1", 9},
		{0x12, 8, "[2001:db8:1:2::a]:1", 17},
		{0x13, 1, "[2001:db8:1:2::b]:1", 17},
	}
	for _, st := range steps {
		newMACs(st.prefix, st.n, st.remote)
		if got := countDiscovered(t, s); got != st.want {
			t.Errorf("after %d new MACs from %s: count = %d, want %d", st.n, st.remote, got, st.want)
		}
	}
}

func TestBootScriptRefusesWhenFull(t *testing.T) {
	s := servingStore(t)
	for i := range maxDiscoveredHosts {
		mac := fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256)
		if _, err := s.InsertDiscoveredHost(t.Context(), mac, "", "", maxDiscoveredHosts); err != nil {
			t.Errorf("prefill %s: %v", mac, err)
			continue
		}
	}
	h := bootScriptHandler(s, noARPFn)
	rr := getBoot(t, h, http.MethodGet, "/booty.ipxe?mac=aa:bb:cc:dd:ee:b1", "10.0.0.40:1")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "not yet approved") {
		t.Fatalf("status %d body %q, want 200 holding script", rr.Code, rr.Body.String())
	}
	if got := countDiscovered(t, s); got != maxDiscoveredHosts {
		t.Errorf("count = %d, want %d", got, maxDiscoveredHosts)
	}
	rows, err := s.ListDiscoveredHosts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range rows {
		if r.MAC == "02:00:00:00:00:00" {
			found = true
		}
		if r.MAC == "aa:bb:cc:dd:ee:b1" {
			t.Error("new MAC was recorded into a full table")
		}
	}
	if !found {
		t.Error("02:00:00:00:00:00 was evicted from the full table")
	}
}

func TestBootScriptRecordsNothingForRegisteredMAC(t *testing.T) {
	s := servingStore(t)
	const mac = "aa:bb:cc:dd:ee:b2"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac}); err != nil {
		t.Fatal(err)
	}
	getBoot(t, bootScriptHandler(s, noARPFn), http.MethodGet, "/booty.ipxe?mac="+mac, "10.0.0.7:5")
	if got := countDiscovered(t, s); got != 0 {
		t.Errorf("count = %d, want 0 for a registered MAC", got)
	}
}

// TestBootScriptRecordsNothingOnLookupError: a known host hitting a transient
// DB error must not be filed as discovered. Closing the hardware store makes
// GetMacAddress fail with "database is closed", which is not ErrNotFound (a
// nil hardware store would read as ErrNotFound).
func TestBootScriptRecordsNothingOnLookupError(t *testing.T) {
	hw := servingStore(t)
	rec, err := db.Open(filepath.Join(t.TempDir(), "rec.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rec.Close() })
	if err := hw.Close(); err != nil {
		t.Fatal(err)
	}
	rr := getBoot(t, bootScriptHandler(rec, noARPFn), http.MethodGet, "/booty.ipxe?mac=aa:bb:cc:dd:ee:b5", "10.0.0.7:5")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := countDiscovered(t, rec); got != 0 {
		t.Errorf("count = %d, want 0 after a lookup error", got)
	}
}

func TestBootScriptHeadDoesNotRecord(t *testing.T) {
	s := servingStore(t)
	getBoot(t, bootScriptHandler(s, noARPFn), http.MethodHead, "/booty.ipxe?mac=aa:bb:cc:dd:ee:b6", "10.0.0.7:5")
	if got := countDiscovered(t, s); got != 0 {
		t.Errorf("count = %d, want 0 after HEAD", got)
	}
}

func TestBootScriptNilStoreSkipsRecording(t *testing.T) {
	servingStore(t)
	rr := getBoot(t, bootScriptHandler(nil, noARPFn), http.MethodGet, "/booty.ipxe?mac=aa:bb:cc:dd:ee:b7", "10.0.0.7:5")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestNonBootRoutesDoNotRecord(t *testing.T) {
	s := servingStore(t)
	dataRoot, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dataRoot.Close() })
	mux := mustBaseMux(t, APIDeps{Store: s}, dataRoot)
	for _, target := range []string{
		"/ignition.json?mac=aa:bb:cc:dd:ee:c1",
		"/machineconfig?mac=aa:bb:cc:dd:ee:c2",
		"/preseed?mac=aa:bb:cc:dd:ee:c3",
		"/menu/talos/x/amd64/v1/boot.ipxe?mac=aa:bb:cc:dd:ee:c4",
	} {
		getBoot(t, mux, http.MethodGet, target, "10.0.0.7:5")
	}
	if got := countDiscovered(t, s); got != 0 {
		t.Errorf("count = %d, want 0: only /booty.ipxe records", got)
	}
}
