package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/hardware"
)

// refusalRecords counts the config-refusal records at Warn and at Debug.
func refusalRecords(logs *bytes.Buffer) (warn, debug int) {
	b := logs.Bytes()
	return bytes.Count(b, []byte(`level=WARN msg="config refused: no valid boot ticket"`)),
		bytes.Count(b, []byte(`level=DEBUG msg="config refused: no valid boot ticket"`))
}

// TestConfigRefusalWarnIsBoundedPerSource: the config routes take no
// credential, so one source can be refused as often as it likes. Through the
// real baseMux, only the first 3 refusals a minute from one source log at Warn;
// the rest log at Debug, and a second source keeps its own budget, so a noisy
// device cannot mute a real host's expired-ticket warning.
func TestConfigRefusalWarnIsBoundedPerSource(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:7e"
	rt := routeFor(t, "/machineconfig")
	s, marker := seedTicketHost(t, rt, mac, false)
	mux := mustBaseMux(t, APIDeps{Store: s, Tickets: testTicketer(t)}, testDataRoot(t, t.TempDir()))
	logBuf := captureLogs(t)
	refuse := func(remote string) {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, rt.path+"?mac="+mac, http.NoBody)
		req.RemoteAddr = remote
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		checkRefused(t, rt.path, rr, marker)
	}

	for range 4 {
		refuse("192.0.2.10:1234")
	}
	if warn, debug := refusalRecords(logBuf); warn != 3 || debug != 1 {
		t.Errorf("one source, 4 refusals: Warn = %d, Debug = %d, want 3 and 1:\n%s", warn, debug, logBuf)
	}

	logBuf.Reset()
	refuse("192.0.2.11:1234")
	if warn, debug := refusalRecords(logBuf); warn != 1 || debug != 0 {
		t.Errorf("second source: Warn = %d, Debug = %d, want 1 and 0:\n%s", warn, debug, logBuf)
	}
}

// TestConfigRefusalLogsNoError: a 404 refusal is the expected answer to a
// caller without an approved host or a valid ticket, not a booty failure, and
// an unknown MAC never reaches the throttled Warn. So the refusal writes no
// ERROR record, or any client could write one per request without limit.
func TestConfigRefusalLogsNoError(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:7f"
	for _, path := range []string{"/machineconfig", "/preseed"} {
		for _, c := range []struct {
			name     string
			register bool
		}{
			{"unknown MAC", false},
			{"approved host without a ticket", true},
		} {
			t.Run(path[1:]+"/"+c.name, func(t *testing.T) {
				rt := routeFor(t, path)
				s, marker := seedTicketHost(t, rt, mac, false)
				target := path + "?mac=" + mac
				if !c.register {
					target = path + "?mac=aa:bb:cc:dd:ee:70"
				}
				logBuf := captureLogs(t)
				rr := fetchConfig(t, regressHandler(t, s, path, failingARP), target)
				checkRefused(t, path, rr, marker)
				if bytes.Contains(logBuf.Bytes(), []byte("level=ERROR")) {
					t.Errorf("refusal logged at ERROR:\n%s", logBuf)
				}
			})
		}
	}
}

// TestInvalidHostnameLoggedOnlyForProvenHost: ?hostname= is read only for a
// request proven to be an approved host's own boot, so a caller without a
// valid ticket cannot write the invalid-hostname Warn at will (#169). A
// ticketed request still logs it.
func TestInvalidHostnameLoggedOnlyForProvenHost(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:6e"
	const warn = "machineconfig: ignoring invalid hostname"
	for _, c := range []struct {
		name, query string
		ticket      bool
		want        int
	}{
		{"unknown MAC", "?mac=aa:bb:cc:dd:ee:6f&hostname=bad_name", false, 0},
		{"approved host without a ticket", "?mac=" + mac + "&hostname=bad_name", false, 0},
		{"approved host with a ticket", "?mac=" + mac + "&hostname=bad_name", true, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := routeFor(t, "/machineconfig")
			s, _ := seedTicketHost(t, rt, mac, false)
			target := rt.path + c.query
			if c.ticket {
				target = withTicket(t, testTicketer(t), target, mac, "talos")
			}
			logBuf := captureLogs(t)
			fetchConfig(t, regressHandler(t, s, rt.path, failingARP), target)
			if n := bytes.Count(logBuf.Bytes(), []byte(warn)); n != c.want {
				t.Errorf("%q records = %d, want %d:\n%s", warn, n, c.want, logBuf)
			}
		})
	}
}

// TestUnapprovedMemberRefusalLogsNoError: the 403 for an unapproved cluster
// member comes before any ticket check, so a client naming a revoked member's
// MAC could write one ERROR per request. It is the expected answer, so the
// refusal writes no ERROR record (#169); the 403 and its body are unchanged.
func TestUnapprovedMemberRefusalLogsNoError(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:6d"
	s := servingStore(t)
	testSecretsKey(t)
	seedMember(t, s, mac)
	if err := hardware.Revoke(mac); err != nil {
		t.Fatal(err)
	}
	logBuf := captureLogs(t)
	rr := fetchConfig(t, regressHandler(t, s, "/machineconfig", failingARP), "/machineconfig?mac="+mac)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "cluster member config requires an approved host") {
		t.Fatalf("got %d %q, want the 403 refusal", rr.Code, rr.Body.String())
	}
	if bytes.Contains(logBuf.Bytes(), []byte("level=ERROR")) {
		t.Errorf("refusal logged at ERROR:\n%s", logBuf)
	}
}
