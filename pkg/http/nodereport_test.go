package http

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/nodereport"
)

const reportBody = `{"running":"4152.2.0","kernel":"6.6","firmware":"uefi"}`

func nodeReportKey() []byte { return bytes.Repeat([]byte{1}, 32) }

func nodeReportMAC(row int) string { return fmt.Sprintf("aa:bb:cc:dd:ee:c%d", row) }

func nodeReportClock() func() time.Time {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return at }
}

func seedReportHost(t *testing.T, s *db.Store, mac string) {
	t.Helper()
	if err := s.UpsertHost(db.Host{MAC: mac}); err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
}

// rowARP answers 10.1.0.<n> with nodeReportMAC(n), so each table row's
// RemoteAddr ARP-resolves to its own MAC. Any other address fails.
func rowARP(ip net.IP) (net.HardwareAddr, error) {
	if v4 := ip.To4(); v4 != nil && v4[0] == 10 && v4[1] == 1 && v4[2] == 0 {
		return net.ParseMAC(nodeReportMAC(int(v4[3])))
	}
	return nil, errors.New("no reply")
}

func postReport(rr *reportReceiver, remote, query, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/node/report"+query, strings.NewReader(body))
	req.RemoteAddr = remote
	if token != "" {
		req.Header.Set("X-Booty-Node-Token", token)
	}
	w := httptest.NewRecorder()
	rr.ServeHTTP(w, req)
	return w
}

func TestNodeReportStatuses(t *testing.T) {
	key := nodeReportKey()
	tests := []struct {
		name string
		want int
		// run returns the recorder of the last request.
		run func(t *testing.T, rr *reportReceiver, s *db.Store, row int) *httptest.ResponseRecorder
	}{
		{
			"malformed MAC",
			http.StatusBadRequest,
			func(_ *testing.T, rr *reportReceiver, _ *db.Store, row int) *httptest.ResponseRecorder {
				return postReport(rr, fmt.Sprintf("10.1.0.%d:1000", row), "?mac=zz", "x", reportBody)
			},
		},
		{
			"missing token header",
			http.StatusUnauthorized,
			func(_ *testing.T, rr *reportReceiver, _ *db.Store, row int) *httptest.ResponseRecorder {
				return postReport(rr, fmt.Sprintf("10.1.0.%d:1000", row), "?mac="+nodeReportMAC(row), "", reportBody)
			},
		},
		{
			"unknown MAC",
			http.StatusUnauthorized,
			func(_ *testing.T, rr *reportReceiver, _ *db.Store, row int) *httptest.ResponseRecorder {
				mac := "aa:bb:cc:dd:ee:ff"
				return postReport(
					rr,
					fmt.Sprintf("10.1.0.%d:1000", row),
					"?mac="+mac,
					nodereport.Token(key, mac, 0),
					reportBody,
				)
			},
		},
		{
			"token for another host",
			http.StatusUnauthorized,
			func(t *testing.T, rr *reportReceiver, s *db.Store, row int) *httptest.ResponseRecorder {
				seedReportHost(t, s, nodeReportMAC(0))
				return postReport(
					rr,
					fmt.Sprintf("10.1.0.%d:1000", row),
					"?mac="+nodeReportMAC(0),
					nodereport.Token(key, nodeReportMAC(1), 0),
					reportBody,
				)
			},
		},
		{
			"valid",
			http.StatusNoContent,
			func(t *testing.T, rr *reportReceiver, s *db.Store, row int) *httptest.ResponseRecorder {
				mac := nodeReportMAC(row)
				seedReportHost(t, s, mac)
				w := postReport(
					rr,
					fmt.Sprintf("10.1.0.%d:1000", row),
					"?mac="+mac,
					nodereport.Token(key, mac, 0),
					reportBody,
				)
				at, rep, err := s.GetHostHealth(mac)
				if err != nil {
					t.Fatalf("GetHostHealth: %v", err)
				}
				if at != "2026-10-11T12:00:00Z" || !strings.Contains(string(rep), `"running":"4152.2.0"`) ||
					!strings.Contains(string(rep), `"receivedAt":"2026-10-11T12:00:00Z"`) {
					t.Fatalf("stored (%q, %s)", at, rep)
				}
				return w
			},
		},
		{
			"second valid report within 10 s",
			http.StatusTooManyRequests,
			func(t *testing.T, rr *reportReceiver, s *db.Store, row int) *httptest.ResponseRecorder {
				mac := nodeReportMAC(row)
				seedReportHost(t, s, mac)
				remote := fmt.Sprintf("10.1.0.%d:1000", row)
				if first := postReport(
					rr,
					remote,
					"?mac="+mac,
					nodereport.Token(key, mac, 0),
					reportBody,
				); first.Code != http.StatusNoContent {
					t.Fatalf("first report = %d, want 204", first.Code)
				}
				return postReport(rr, remote, "?mac="+mac, nodereport.Token(key, mac, 0), reportBody)
			},
		},
		{
			"body over 64 KiB",
			http.StatusRequestEntityTooLarge,
			func(t *testing.T, rr *reportReceiver, s *db.Store, row int) *httptest.ResponseRecorder {
				mac := nodeReportMAC(row)
				seedReportHost(t, s, mac)
				big := `{"running":"` + strings.Repeat("a", 70<<10) + `"}`
				return postReport(
					rr,
					fmt.Sprintf("10.1.0.%d:1000", row),
					"?mac="+mac,
					nodereport.Token(key, mac, 0),
					big,
				)
			},
		},
		{
			"bad firmware",
			http.StatusBadRequest,
			func(t *testing.T, rr *reportReceiver, s *db.Store, row int) *httptest.ResponseRecorder {
				mac := nodeReportMAC(row)
				seedReportHost(t, s, mac)
				return postReport(
					rr,
					fmt.Sprintf("10.1.0.%d:1000", row),
					"?mac="+mac,
					nodereport.Token(key, mac, 0),
					`{"firmware":"coreboot"}`,
				)
			},
		},
		{
			"undecodable JSON",
			http.StatusBadRequest,
			func(t *testing.T, rr *reportReceiver, s *db.Store, row int) *httptest.ResponseRecorder {
				mac := nodeReportMAC(row)
				seedReportHost(t, s, mac)
				return postReport(
					rr,
					fmt.Sprintf("10.1.0.%d:1000", row),
					"?mac="+mac,
					nodereport.Token(key, mac, 0),
					`{`,
				)
			},
		},
		{
			"stale token after bump",
			http.StatusUnauthorized,
			func(t *testing.T, rr *reportReceiver, s *db.Store, row int) *httptest.ResponseRecorder {
				mac := nodeReportMAC(row)
				seedReportHost(t, s, mac)
				if _, err := s.BumpHostReportTokenGen(mac); err != nil {
					t.Fatal(err)
				}
				return postReport(
					rr,
					fmt.Sprintf("10.1.0.%d:1000", row),
					"?mac="+mac,
					nodereport.Token(key, mac, 0),
					reportBody,
				)
			},
		},
		{
			"valid token, ARP names another MAC",
			http.StatusUnauthorized,
			func(t *testing.T, rr *reportReceiver, s *db.Store, row int) *httptest.ResponseRecorder {
				mac := nodeReportMAC(0)
				seedReportHost(t, s, mac)
				rr.arp = fixedARP("aa:bb:cc:dd:ee:99")
				return postReport(
					rr,
					fmt.Sprintf("10.1.0.%d:1000", row),
					"?mac="+mac,
					nodereport.Token(key, mac, 0),
					reportBody,
				)
			},
		},
		{
			"valid token, ARP fails",
			http.StatusUnauthorized,
			func(t *testing.T, rr *reportReceiver, s *db.Store, row int) *httptest.ResponseRecorder {
				mac := nodeReportMAC(0)
				seedReportHost(t, s, mac)
				rr.arp = failingARP
				return postReport(
					rr,
					fmt.Sprintf("10.1.0.%d:1000", row),
					"?mac="+mac,
					nodereport.Token(key, mac, 0),
					reportBody,
				)
			},
		},
	}
	for i, tc := range tests {
		row := i // MAC aa:bb:cc:dd:ee:c<row>, RemoteAddr 10.1.0.<row>
		t.Run(tc.name, func(t *testing.T) {
			s := servingStore(t)
			seedReportHost(t, s, nodeReportMAC(0))
			seedReportHost(t, s, nodeReportMAC(1))
			rr := newReportReceiver(s, key, nodeReportClock(), rowARP)
			if w := tc.run(t, rr, s, row); w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestNodeReportMethodNotAllowed(t *testing.T) {
	s := servingStore(t)
	rr := newReportReceiver(s, nodeReportKey(), nodeReportClock(), rowARP)
	w := httptest.NewRecorder()
	rr.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/node/report?mac="+nodeReportMAC(0), nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
}

func TestNodeReportFeatureOff(t *testing.T) {
	h := nodeReportHandler(APIDeps{})
	w := httptest.NewRecorder()
	h.ServeHTTP(
		w,
		httptest.NewRequest(http.MethodPost, "/node/report?mac="+nodeReportMAC(0), strings.NewReader(reportBody)),
	)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestNodeReportHandlerStoresReceiver(t *testing.T) {
	s := servingStore(t)
	nr := &NodeReports{Key: nodeReportKey()}
	if _, ok := nodeReportHandler(APIDeps{Store: s, NodeReports: nr}).(*reportReceiver); !ok {
		t.Fatal("handler is not a *reportReceiver")
	}
	if nr.receiver == nil {
		t.Fatal("NodeReports.receiver not set")
	}
}

// A real host with another host's token must be indistinguishable from an
// unknown MAC: same status, same body.
func TestNodeReport401BodiesIdentical(t *testing.T) {
	key := nodeReportKey()
	s := servingStore(t)
	seedReportHost(t, s, nodeReportMAC(0))
	seedReportHost(t, s, nodeReportMAC(1))
	rr := newReportReceiver(s, key, nodeReportClock(), rowARP)

	missing := postReport(rr, "10.1.0.1:1000", "?mac="+nodeReportMAC(0), "", reportBody)
	unknown := postReport(
		rr,
		"10.1.0.2:1000",
		"?mac=aa:bb:cc:dd:ee:ff",
		nodereport.Token(key, "aa:bb:cc:dd:ee:ff", 0),
		reportBody,
	)
	other := postReport(
		rr,
		"10.1.0.3:1000",
		"?mac="+nodeReportMAC(0),
		nodereport.Token(key, nodeReportMAC(1), 0),
		reportBody,
	)
	garbage := postReport(rr, "10.1.0.4:1000", "?mac="+nodeReportMAC(0), "not-a-token", reportBody)
	// A valid token from an address that ARP-resolves to another MAC
	// (10.1.0.1 answers as nodeReportMAC(1)).
	arpMismatch := postReport(
		rr,
		"10.1.0.1:1000",
		"?mac="+nodeReportMAC(0),
		nodereport.Token(key, nodeReportMAC(0), 0),
		reportBody,
	)
	for name, w := range map[string]*httptest.ResponseRecorder{
		"missing":      missing,
		"unknown":      unknown,
		"other":        other,
		"garbage":      garbage,
		"arp mismatch": arpMismatch,
	} {
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, w.Code)
		}
		if w.Body.String() != missing.Body.String() {
			t.Errorf("%s: body %q differs from %q", name, w.Body.String(), missing.Body.String())
		}
	}
	if missing.Body.String() != "Unauthorized\n" {
		t.Errorf("401 body = %q, want %q", missing.Body.String(), "Unauthorized\n")
	}
}

// Random MACs from one IP must hit the per-IP limit before any DB lookup.
func TestNodeReportFloodHitsPerIPLimitBeforeDB(t *testing.T) {
	s := servingStore(t)
	rr := newReportReceiver(s, nodeReportKey(), nodeReportClock(), rowARP)
	for i := 1; i <= 10; i++ {
		mac := fmt.Sprintf("aa:bb:cc:00:00:%02x", i)
		if w := postReport(
			rr,
			"10.1.0.9:1000",
			"?mac="+mac,
			"anything",
			reportBody,
		); w.Code != http.StatusUnauthorized {
			t.Fatalf("request %d = %d, want 401", i, w.Code)
		}
	}
	// Close the store: a DB lookup would now fail, but the 11th must be
	// rejected by the limiter without reaching it.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if w := postReport(
		rr,
		"10.1.0.9:1000",
		"?mac=aa:bb:cc:00:00:ff",
		"anything",
		reportBody,
	); w.Code != http.StatusTooManyRequests {
		t.Fatalf("11th request = %d, want 429", w.Code)
	}
}

func TestNodeReportLimiterMapsAreBounded(t *testing.T) {
	s := servingStore(t)
	rr := newReportReceiver(s, nodeReportKey(), nodeReportClock(), rowARP)
	for i := range 5000 {
		remote := fmt.Sprintf("10.%d.%d.%d:1000", i>>16&0xff, i>>8&0xff, i&0xff)
		postReport(rr, remote, "?mac="+nodeReportMAC(0), "anything", reportBody)
	}
	if n := rr.perIP.Len(); n > 4096 {
		t.Fatalf("perIP.Len() = %d, want <= 4096", n)
	}
}

func TestGenCache(t *testing.T) {
	s := servingStore(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	c := newGenCache(s, clock)
	mac := nodeReportMAC(0)

	if _, err := c.Lookup(mac); err == nil {
		t.Fatal("absent host: want error")
	}
	if len(c.m) != 0 {
		t.Fatal("absent host was cached")
	}
	seedReportHost(t, s, mac)
	if g, err := c.Lookup(mac); err != nil || g != 0 {
		t.Fatalf("gen = %d, %v; want 0", g, err)
	}
	if _, err := s.BumpHostReportTokenGen(mac); err != nil {
		t.Fatal(err)
	}
	if g, _ := c.Lookup(mac); g != 0 {
		t.Fatalf("cached gen = %d, want 0 within TTL", g)
	}
	c.Set(mac, 1)
	if g, _ := c.Lookup(mac); g != 1 {
		t.Fatalf("gen after Set = %d, want 1", g)
	}
	if _, err := s.BumpHostReportTokenGen(mac); err != nil {
		t.Fatal(err)
	}
	now = now.Add(61 * time.Second)
	if g, _ := c.Lookup(mac); g != 2 {
		t.Fatalf("gen after TTL = %d, want 2", g)
	}
}

func TestGenCacheCapacity(t *testing.T) {
	c := newGenCache(nil, nodeReportClock())
	exp := nodeReportClock()().Add(time.Minute)
	for i := range 5000 {
		c.put(fmt.Sprintf("m%d", i), 0, exp)
	}
	if len(c.m) > 4096 {
		t.Fatalf("genCache holds %d entries, want <= 4096", len(c.m))
	}
}

// TestGenCacheLateLookupCannotResurrectOldGeneration replays the rotation
// race: a lookup read generation 0, the token was then rotated (DB bumped,
// cache Set to 1), and only afterwards did the lookup's put(0) arrive.
func TestGenCacheLateLookupCannotResurrectOldGeneration(t *testing.T) {
	s := servingStore(t)
	key := nodeReportKey()
	now := nodeReportClock()
	mac := nodeReportMAC(1)
	rr := newReportReceiver(s, key, now, fixedARP(mac))
	seedReportHost(t, s, mac)

	oldGen, err := rr.gens.Lookup(mac) // the in-flight lookup read generation N
	if err != nil || oldGen != 0 {
		t.Fatalf("Lookup = %d, %v", oldGen, err)
	}
	newGen, err := s.BumpHostReportTokenGen(mac) // rotate
	if err != nil {
		t.Fatal(err)
	}
	rr.gens.Set(mac, newGen)
	rr.gens.put(mac, oldGen, now().Add(genCacheTTL)) // the late put(N)

	if got, lerr := rr.gens.Lookup(mac); lerr != nil || got != newGen {
		t.Fatalf("cached gen = %d, %v, want %d", got, lerr, newGen)
	}
	oldTok, newTok := nodereport.Token(key, mac, oldGen), nodereport.Token(key, mac, newGen)
	if w := postReport(rr, "10.3.0.1:1", "?mac="+mac, oldTok, reportBody); w.Code != http.StatusUnauthorized {
		t.Fatalf("old token = %d, want 401", w.Code)
	}
	if w := postReport(rr, "10.3.0.2:1", "?mac="+mac, newTok, reportBody); w.Code != http.StatusNoContent {
		t.Fatalf("new token = %d, want 204", w.Code)
	}
}

// TestGenCachePutReplacesExpiredHigherGeneration: the never-lower rule covers
// only live entries, so a host deleted and re-added (generation back to 0)
// recovers once the old entry has expired.
func TestGenCachePutReplacesExpiredHigherGeneration(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	c := newGenCache(servingStore(t), func() time.Time { return at })
	c.Set("m", 5)
	at = at.Add(genCacheTTL + time.Second)
	c.put("m", 0, at.Add(genCacheTTL))
	c.mu.Lock()
	got := c.m["m"].gen
	c.mu.Unlock()
	if got != 0 {
		t.Fatalf("gen = %d, want 0 after the higher entry expired", got)
	}
}

func TestGenCacheSetRespectsCapacity(t *testing.T) {
	c := newGenCache(servingStore(t), nodeReportClock())
	for i := range genCacheCapacity + 10 {
		c.Set(fmt.Sprintf("m%d", i), 1)
	}
	c.mu.Lock()
	n := len(c.m)
	c.mu.Unlock()
	if n > genCacheCapacity {
		t.Fatalf("cache holds %d entries, cap %d", n, genCacheCapacity)
	}
}

// TestNodeReportRouteMethods drives the real baseMux: with the feature on a
// non-POST is 405 (not the catch-all's 302); with it off every method is 404.
func TestNodeReportRouteMethods(t *testing.T) {
	s := servingStore(t)
	on := baseMux(APIDeps{Store: s, NodeReports: &NodeReports{Key: nodeReportKey()}})
	off := baseMux(APIDeps{Store: s})
	tests := []struct {
		name   string
		mux    *http.ServeMux
		method string
		want   int
	}{
		{"on GET", on, http.MethodGet, http.StatusMethodNotAllowed},
		{"on PUT", on, http.MethodPut, http.StatusMethodNotAllowed},
		{"on POST", on, http.MethodPost, http.StatusBadRequest}, // no mac: reaches the receiver
		{"off GET", off, http.MethodGet, http.StatusNotFound},
		{"off POST", off, http.MethodPost, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.mux.ServeHTTP(w, httptest.NewRequest(tc.method, "/node/report", nil))
			if w.Code != tc.want {
				t.Fatalf("%s /node/report = %d, want %d", tc.method, w.Code, tc.want)
			}
		})
	}
}
