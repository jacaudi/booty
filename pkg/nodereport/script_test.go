package nodereport

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/coreos/go-systemd/v22/unit"
)

const (
	testMAC   = "aa:bb:cc:dd:ee:01"
	testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func mustScript(t *testing.T, server string) string {
	t.Helper()
	s, err := Script(server, testMAC, testToken)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

type healthBody struct {
	Running       string   `json:"running"`
	FailedUnits   []string `json:"failedUnits"`
	JournalErrors []string `json:"journalErrors"`
	DMI           struct {
		Vendor      string `json:"vendor"`
		Product     string `json:"product"`
		BIOSVersion string `json:"biosVersion"`
		ProductUUID string `json:"productUUID"`
	} `json:"dmi"`
	Firmware string `json:"firmware"`
	Kernel   string `json:"kernel"`
	BootID   string `json:"bootID"`
}

// TestHealthReportScript runs the script under bash with stubbed
// journalctl/systemctl, a fake os-release and sysfs/procfs trees, and
// checks the JSON the real curl POSTs: escaping of quotes, backslashes,
// tabs and control characters, the per-line and total journal caps, and
// every field.
func TestHealthReportScript(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "uname"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	var (
		mu     sync.Mutex
		bodies []string
		macs   []string
		ctype  []string
		tokens []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/node/report" {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		macs = append(macs, r.URL.Query().Get("mac"))
		ctype = append(ctype, r.Header.Get("Content-Type"))
		tokens = append(tokens, r.Header.Get("X-Booty-Node-Token"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	stubs := filepath.Join(dir, "bin")
	if err := os.Mkdir(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	longLine := strings.Repeat("x", 500)
	var journal []string
	for range 60 {
		journal = append(journal, longLine)
	}
	journal = append(journal,
		"2026-09-28T10:00:00+0000 kernel: quote \" backslash \\ tab\tend",
		"2026-09-28T10:00:01+0000 sshd[12]: bell \x01 and \x1f here")
	writeStub(
		t,
		stubs,
		"journalctl",
		`[ "$*" = "-p err -b -o short-iso --no-hostname --no-pager -n 50" ] || { echo "bad args: $*" >&2; exit 2; }
cat <<'EOF'
`+strings.Join(journal, "\n")+`
EOF
`,
	)
	writeStub(
		t,
		stubs,
		"systemctl",
		`[ "$*" = "list-units --state=failed --plain --no-legend" ] || { echo "bad args: $*" >&2; exit 2; }
printf '%s\n' "booty-k8s-join.service loaded failed failed Join" "var-lib-containerd.mount loaded failed failed Mount"
`,
	)
	writeStub(t, stubs, "ip", `echo "script must not discover the MAC itself" >&2; exit 2`)
	writeFile(
		t,
		filepath.Join(dir, "os-release"),
		"NAME=\"Bluefin Server\"\nID=bluefin-server\nVERSION=\"26.09.673\"\nVERSION_ID=26.09.673\n",
	)
	sys := filepath.Join(dir, "sys")
	writeFile(t, filepath.Join(sys, "class", "dmi", "id", "sys_vendor"), "HP\n")
	writeFile(t, filepath.Join(sys, "class", "dmi", "id", "product_name"), "HP EliteDesk 800 G1 \"DM\"\n")
	writeFile(t, filepath.Join(sys, "class", "dmi", "id", "bios_version"), "L01 v02.78\n")
	writeFile(t, filepath.Join(sys, "class", "dmi", "id", "product_uuid"), "0CCCFB00-A3F6-11E4-9504-40A8F0AF398D\n")
	if err := os.MkdirAll(filepath.Join(sys, "firmware", "efi"), 0o755); err != nil {
		t.Fatal(err)
	}
	proc := filepath.Join(dir, "proc")
	writeFile(t, filepath.Join(proc, "sys", "kernel", "random", "boot_id"), "5b1c6b2e-4a9e-4e7b-9a0e-2d3f4a5b6c7d\n")
	script := filepath.Join(dir, "health-report")
	writeFile(t, script, mustScript(t, strings.TrimPrefix(srv.URL, "http://")))

	run := func(t *testing.T, extraEnv ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "bash", script)
		cmd.Env = append(append(os.Environ(),
			"PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
			"BOOTY_OS_RELEASE="+filepath.Join(dir, "os-release"),
			"BOOTY_SYS="+sys,
			"BOOTY_PROC="+proc,
			"BOOTY_MAC=aa:bb:cc:dd:ee:01",
		), extraEnv...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("script failed: %v\n%s", err, out)
		}
		return string(out)
	}
	out := run(t)
	if !strings.Contains(out, "health reported: running=26.09.673 failedUnits=2") {
		t.Fatalf("script output: %s", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || macs[0] != "aa:bb:cc:dd:ee:01" || ctype[0] != "application/json" {
		t.Fatalf("bodies=%d macs=%v ctype=%v", len(bodies), macs, ctype)
	}
	if tokens[0] != testToken {
		t.Fatalf("X-Booty-Node-Token = %q, want %s", tokens[0], testToken)
	}
	var got healthBody
	if err := json.Unmarshal([]byte(bodies[0]), &got); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, bodies[0])
	}
	kernel, err := exec.CommandContext(t.Context(), "uname", "-r").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got.Running != "26.09.673" || got.Firmware != "uefi" || got.Kernel != strings.TrimSpace(string(kernel)) ||
		got.BootID != "5b1c6b2e-4a9e-4e7b-9a0e-2d3f4a5b6c7d" {
		t.Fatalf("scalar fields: %+v", got)
	}
	if got.DMI.Vendor != "HP" || got.DMI.Product != `HP EliteDesk 800 G1 "DM"` || got.DMI.BIOSVersion != "L01 v02.78" ||
		got.DMI.ProductUUID != "0CCCFB00-A3F6-11E4-9504-40A8F0AF398D" {
		t.Fatalf("dmi: %+v", got.DMI)
	}
	if !reflect.DeepEqual(got.FailedUnits, []string{"booty-k8s-join.service", "var-lib-containerd.mount"}) {
		t.Fatalf("failedUnits: %v", got.FailedUnits)
	}
	last := got.JournalErrors[len(got.JournalErrors)-1]
	if last != "2026-09-28T10:00:01+0000 sshd[12]: bell \x01 and \x1f here" {
		t.Fatalf("control characters must round-trip through \\u escapes: %q", last)
	}
	prev := got.JournalErrors[len(got.JournalErrors)-2]
	if prev != "2026-09-28T10:00:00+0000 kernel: quote \" backslash \\ tab\tend" {
		t.Fatalf("quotes, backslashes and tabs must round-trip: %q", prev)
	}
	total := 0
	for _, l := range got.JournalErrors[:len(got.JournalErrors)-2] {
		if len(l) != 300 || !strings.HasPrefix(l, "xxx") {
			t.Errorf("lines must be clipped to 300 chars, got %d", len(l))
			continue
		}
		total += len(l)
	}
	if n := len(got.JournalErrors); n > 50 || total+len(last)+len(prev) > 8192 {
		t.Fatalf("journal cap: %d lines, %d bytes", n, total+len(last)+len(prev))
	}
	if total+len(last)+len(prev)+300 <= 8192 {
		t.Fatalf(
			"the newest lines must fill the 8 KiB budget: %d lines, %d bytes",
			len(got.JournalErrors),
			total+len(last)+len(prev),
		)
	}
}

func TestHealthReportScriptEmptyLists(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	stubs := filepath.Join(dir, "bin")
	if err := os.Mkdir(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, stubs, "journalctl", "exit 0")
	writeStub(t, stubs, "systemctl", "exit 0")
	writeFile(t, filepath.Join(dir, "os-release"), "ID=flatcar\nVERSION_ID=4757.2.0\n")
	script := filepath.Join(dir, "health-report")
	writeFile(t, script, mustScript(t, strings.TrimPrefix(srv.URL, "http://")))
	cmd := exec.CommandContext(t.Context(), "bash", script)
	cmd.Env = append(
		os.Environ(),
		"PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BOOTY_OS_RELEASE="+filepath.Join(dir, "os-release"),
		"BOOTY_SYS="+filepath.Join(dir, "nosys"),
		"BOOTY_PROC="+filepath.Join(dir, "noproc"),
		"BOOTY_MAC=aa:bb:cc:dd:ee:02",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var got healthBody
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	if got.Running != "4757.2.0" || got.Firmware != "bios" || len(got.FailedUnits) != 0 ||
		len(got.JournalErrors) != 0 ||
		got.DMI.Vendor != "" ||
		got.BootID != "" {
		t.Fatalf("%+v", got)
	}
}

func TestHealthReportScriptFailsWhenBootyRefuses(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	srv := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusNotFound) }),
	)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	stubs := filepath.Join(dir, "bin")
	if err := os.Mkdir(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, stubs, "journalctl", "exit 0")
	writeStub(t, stubs, "systemctl", "exit 0")
	writeFile(t, filepath.Join(dir, "os-release"), "ID=flatcar\nVERSION_ID=4757.2.0\n")
	script := filepath.Join(dir, "health-report")
	writeFile(t, script, mustScript(t, strings.TrimPrefix(srv.URL, "http://")))
	cmd := exec.CommandContext(t.Context(), "bash", script)
	cmd.Env = append(
		os.Environ(),
		"PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BOOTY_OS_RELEASE="+filepath.Join(dir, "os-release"),
		"BOOTY_MAC=aa:bb:cc:dd:ee:02",
	)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "refused the health report") {
		t.Fatalf("a refused POST must exit non-zero so systemd retries: err=%v\n%s", err, out)
	}
}

func TestHealthUnitParses(t *testing.T) {
	opts, err := unit.DeserializeOptions(strings.NewReader(Unit(ScriptPath)))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Unit/After":               "multi-user.target network-online.target",
		"Unit/DefaultDependencies": "no",
		"Service/Type":             "oneshot",
		"Service/RemainAfterExit":  "yes",
		"Service/Restart":          "on-failure",
		"Service/RestartSec":       "30s",
		"Service/ExecStart":        ScriptPath,
		"Install/WantedBy":         "multi-user.target",
		"Unit/Conflicts":           "shutdown.target",
		"Unit/Before":              "shutdown.target",
	}
	got := map[string]string{}
	for _, o := range opts {
		got[o.Section+"/"+o.Name] = o.Value
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// TestScriptUsesEmbeddedMAC runs the script without BOOTY_MAC and checks the
// posted ?mac= is the value baked in by Script.
func TestScriptUsesEmbeddedMAC(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	var mac string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mac = r.URL.Query().Get("mac")
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	stubs := filepath.Join(dir, "bin")
	if err := os.Mkdir(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStub(t, stubs, "journalctl", "exit 0")
	writeStub(t, stubs, "systemctl", "exit 0")
	writeFile(t, filepath.Join(dir, "os-release"), "ID=flatcar\nVERSION_ID=4757.2.0\n")
	script := filepath.Join(dir, "health-report")
	writeFile(t, script, mustScript(t, strings.TrimPrefix(srv.URL, "http://")))
	cmd := exec.CommandContext(t.Context(), "bash", script)
	cmd.Env = append(os.Environ(), "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BOOTY_OS_RELEASE="+filepath.Join(dir, "os-release"), "BOOTY_SYS="+filepath.Join(dir, "nosys"),
		"BOOTY_PROC="+filepath.Join(dir, "noproc"), "BOOTY_MAC=")
	cmd.Env = slices.DeleteFunc(cmd.Env, func(e string) bool { return strings.HasPrefix(e, "BOOTY_MAC=") })
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if mac != testMAC {
		t.Fatalf("posted mac = %q, want %q", mac, testMAC)
	}
}

func TestScriptRejectsBadInput(t *testing.T) {
	tests := []struct{ name, server, mac, token string }{
		{"mac injection", "h:1", "x}; touch /tmp/pwn; : ${y", testToken},
		{"mac uppercase", "h:1", "AA:BB:CC:DD:EE:01", testToken},
		{"mac short", "h:1", "aa:bb", testToken},
		{"token quote", "h:1", testMAC, "ab'cd" + testToken[5:]},
		{"token short", "h:1", testMAC, "tok"},
		{"token empty", "h:1", testMAC, ""},
		{"server subshell", `h"$(id)"`, testMAC, testToken},
		{"server empty", "", testMAC, testToken},
		{"server space", "h 1", testMAC, testToken},
		{"server newline", "h\n1", testMAC, testToken},
		{"server backtick", "h`id`", testMAC, testToken},
		{"server dollar", "h$x", testMAC, testToken},
		{"server single quote", "h'1", testMAC, testToken},
		{"server backslash", `h\1`, testMAC, testToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Script(tt.server, tt.mac, tt.token)
			if !errors.Is(err, ErrInvalidScriptInput) || s != "" {
				t.Fatalf("got %v, %q; want ErrInvalidScriptInput and empty script", err, s)
			}
		})
	}
}
