package http

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ignitioncfg "github.com/coreos/ignition/v2/config"
	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"
)

const injectButane = "variant: fcos\nversion: 1.5.0\n"

// injectIgnitionHosts binds a flatcar host to a stored butane config and also
// leaves a file-path default, returning the DB-bound and unbound MACs.
func injectIgnitionHosts(t *testing.T) (s *db.Store, boundMAC, fileMAC string) {
	t.Helper()
	s = servingStore(t)
	viper.Set(config.IgnitionFile, "config/ignition.yaml")
	writeFile(t, "config/ignition.yaml", injectButane)
	boundMAC, fileMAC = "aa:bb:cc:dd:ee:81", "aa:bb:cc:dd:ee:82"
	for _, mac := range []string{boundMAC, fileMAC} {
		if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar", Approved: true}); err != nil {
			t.Fatal(err)
		}
		approveHost(t, mac)
	}
	cid, err := s.CreateConfig("bound-butane", "butane")
	if err != nil {
		t.Fatal(err)
	}
	rid, _, err := s.AddConfigRevision(cid, base64.StdEncoding.EncodeToString([]byte(injectButane)), "sha", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetActiveRevision(cid, rid); err != nil {
		t.Fatal(err)
	}
	if err = hardware.SetHostConfig(boundMAC, &cid); err != nil {
		t.Fatal(err)
	}
	return s, boundMAC, fileMAC
}

func getBody(t *testing.T, h http.HandlerFunc, target string) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody))
	if rr.Code != http.StatusOK {
		t.Fatalf("%s = %d: %s", target, rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

func TestIgnitionUnchangedWhenReportsOff(t *testing.T) {
	s, bound, file := injectIgnitionHosts(t)
	for _, mac := range []string{bound, file} {
		body := getBody(t, ignitionHandler(s, nil, arpLookup, allowProof{}), "/ignition.json?mac="+mac)
		if strings.Contains(body, "booty-health") {
			t.Errorf("%s: reports off but body has booty-health:\n%s", mac, body)
		}
	}
}

func TestIgnitionGetsHealthWhenOn(t *testing.T) {
	s, bound, file := injectIgnitionHosts(t)
	n := &NodeReports{Key: nodeReportKey()}
	for name, mac := range map[string]string{"db-bound": bound, "file": file} {
		t.Run(name, func(t *testing.T) {
			body := getBody(t, ignitionHandler(s, n, arpLookup, allowProof{}), "/ignition.json?mac="+mac)
			if _, rep, err := ignitioncfg.Parse([]byte(body)); err != nil {
				t.Fatalf("Parse: %v (report %s)\n%s", err, rep.String(), body)
			}
			for _, want := range []string{"booty-health.service", "/opt/booty/health-report"} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q:\n%s", want, body)
				}
			}
		})
	}
}

func TestIgnitionNoInjectionForUnknownMAC(t *testing.T) {
	s, _, _ := injectIgnitionHosts(t)
	body := getBody(t, ignitionHandler(s, &NodeReports{Key: nodeReportKey()}, arpLookup, allowProof{}), "/ignition.json?mac=aa:bb:cc:dd:ee:8f")
	if !strings.Contains(body, "ExecStart=reboot") {
		t.Fatalf("unknown MAC did not get the reboot config:\n%s", body)
	}
	if strings.Contains(body, "booty-health") {
		t.Fatalf("unknown MAC got the health unit:\n%s", body)
	}
}

// TestWithNodeReportMergeErrorServesOriginal: when the merge fails (here the
// config is not JSON) the config is served unchanged.
func TestWithNodeReportMergeErrorServesOriginal(t *testing.T) {
	s := servingStore(t)
	const mac = "aa:bb:cc:dd:ee:8e"
	seedReportHost(t, s, mac)
	n := &NodeReports{Key: nodeReportKey()}
	host := &hardware.Host{MAC: mac}
	if _, _, ok := n.script(t.Context(), s, host); !ok {
		t.Fatal("precondition: script not available for a known host")
	}
	in := []byte("not an ignition config")
	if got := withNodeReport(t.Context(), n, s, host, in); !bytes.Equal(got, in) {
		t.Fatalf("withNodeReport = %q, want the original %q", got, in)
	}
}

func injectDebianHost(t *testing.T, src string) (*db.Store, string) {
	t.Helper()
	s := servingStore(t)
	viper.Set(config.PreseedFile, "config/preseed.cfg")
	writeFile(t, "config/preseed.cfg", "d-i raw-file-default string x\n")
	const mac = "aa:bb:cc:dd:ee:83"
	host := hardware.Host{MAC: mac, OS: "debian", Hostname: "deb", Approved: true}
	if err := hardware.WriteMacAddress(mac, host); err != nil {
		t.Fatal(err)
	}
	approveHost(t, mac)
	cid, err := s.CreateConfig("deb", "debianconfig")
	if err != nil {
		t.Fatal(err)
	}
	rid, _, err := s.AddConfigRevision(cid, base64.StdEncoding.EncodeToString([]byte(src)), "sha", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetActiveRevision(cid, rid); err != nil {
		t.Fatal(err)
	}
	if err = hardware.SetHostConfig(mac, &cid); err != nil {
		t.Fatal(err)
	}
	return s, mac
}

func TestDebianconfigGetsHealthWhenOn(t *testing.T) {
	s, mac := injectDebianHost(t,
		"disk:\n  devices: [/dev/sda, /dev/sdb]\n  raid: mirror\nlate_command: echo operator-last\npackages: [vim]\n")
	body := getBody(t, preseedHandler(s, &NodeReports{Key: nodeReportKey()}, arpLookup, allowProof{}), "/preseed?mac="+mac)

	// The unit enable is inside base64, so check the decoded late_command.
	health := strings.Index(body, "in-target sh -c 'mkdir -p /opt/booty")
	esp := strings.Index(body, "mkfs.vfat")
	op := strings.Index(body, "echo operator-last")
	if health < 0 || esp < 0 || op < 0 {
		t.Fatalf("missing fragment (health=%d esp=%d op=%d):\n%s", health, esp, op, body)
	}
	if esp >= health || health >= op {
		t.Fatalf("order must be ESP sync, health, operator (esp=%d health=%d op=%d)", esp, health, op)
	}
	if !strings.Contains(body, "systemctl enable booty-health.service") {
		t.Fatalf("missing systemctl enable booty-health.service:\n%s", body)
	}
	var pkgLine string
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "d-i pkgsel/include") {
			pkgLine = line
		}
	}
	if !strings.Contains(pkgLine, "curl") || !strings.Contains(pkgLine, "vim") {
		t.Fatalf("pkgsel/include = %q, want curl and vim", pkgLine)
	}
}

func TestDebianconfigUnchangedWhenReportsOff(t *testing.T) {
	s, mac := injectDebianHost(t, "locale: en_US\n")
	body := getBody(t, preseedHandler(s, nil, arpLookup, allowProof{}), "/preseed?mac="+mac)
	if strings.Contains(body, "booty-health") || strings.Contains(body, "curl") {
		t.Fatalf("reports off but preseed changed:\n%s", body)
	}
}

func TestDebianPreviewAndConverterUnchanged(t *testing.T) {
	out, _, _, err := renderConfig("debianconfig", []byte("locale: en_US\n"), TemplateVars{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "booty-health") {
		t.Fatalf("renderConfig output has booty-health:\n%s", out)
	}
	conv, _, err := ConvertPreseedToDebianConfig([]byte("d-i debian-installer/locale string en_US\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(conv), "booty-health") {
		t.Fatalf("converter output has booty-health:\n%s", conv)
	}
}

func TestRawPreseedNeverInjected(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.PreseedFile, "config/preseed.cfg")
	writeFile(t, "config/preseed.cfg", "d-i raw-file-default string x\n")
	const mac = "aa:bb:cc:dd:ee:84"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "debian", Approved: true}); err != nil {
		t.Fatal(err)
	}
	approveHost(t, mac)
	body := getBody(t, preseedHandler(s, &NodeReports{Key: nodeReportKey()}, arpLookup, allowProof{}), "/preseed?mac="+mac)
	if strings.Contains(body, "booty-health") {
		t.Fatalf("raw preseed was injected:\n%s", body)
	}
}
