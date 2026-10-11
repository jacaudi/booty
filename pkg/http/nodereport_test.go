package http

import (
	"bytes"
	"fmt"
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
	}
	for i, tc := range tests {
		row := i // MAC aa:bb:cc:dd:ee:c<row>, RemoteAddr 10.1.0.<row>
		t.Run(tc.name, func(t *testing.T) {
			s := servingStore(t)
			seedReportHost(t, s, nodeReportMAC(0))
			seedReportHost(t, s, nodeReportMAC(1))
			rr := newReportReceiver(s, key, nodeReportClock())
			if w := tc.run(t, rr, s, row); w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestNodeReportMethodNotAllowed(t *testing.T) {
	s := servingStore(t)
	rr := newReportReceiver(s, nodeReportKey(), nodeReportClock())
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
	rr := newReportReceiver(s, key, nodeReportClock())

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
	for name, w := range map[string]*httptest.ResponseRecorder{"missing": missing, "unknown": unknown, "other": other, "garbage": garbage} {
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
	rr := newReportReceiver(s, nodeReportKey(), nodeReportClock())
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
	rr := newReportReceiver(s, nodeReportKey(), nodeReportClock())
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
	c.Invalidate(mac)
	if g, _ := c.Lookup(mac); g != 1 {
		t.Fatalf("gen after Invalidate = %d, want 1", g)
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
