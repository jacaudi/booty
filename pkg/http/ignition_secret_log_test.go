package http

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

// joinSentinel stands in for the kubeadm join string (--joinString), a secret
// the ignition template is rendered with.
const joinSentinel = "SENTINEL-JOIN-7f3a9c0d1e2b"

// TestIgnitionFailureNeverLogsTheJoinString drives /ignition.json with templates
// that render .JoinString and then fail (or warn in) Butane translation, on
// both the file path (serveFileIgnition) and the DB-bound path
// (serveBoundIgnition). Butane quotes template content back in its errors and
// report entries, and the file path used to dump the whole rendered template,
// so each case reaches a distinct log site. The join string must appear in no
// log record, while on the file path the operator detail (Butane's message
// and line/col) still does.
func TestIgnitionFailureNeverLogsTheJoinString(t *testing.T) {
	cases := []struct {
		name       string
		tmpl       string
		wantStatus int
		wantInLog  string // operator-facing detail the file path must still log
	}{
		// TranslateBytes error whose text quotes the value, plus the
		// rendered-template dump on the error path.
		{"translate error quotes the value", "variant: {{.JoinString}}\nversion: 1.5.0\n", http.StatusInternalServerError, "No translator exists for variant"},
		// TranslateBytes yaml error: only the template dump carried the value.
		{"yaml syntax error", "variant: fcos\nversion: 1.5.0\nnot: valid: {{.JoinString}}\n", http.StatusInternalServerError, "mapping values are not allowed"},
		// Fatal report: only the template dump carried the value.
		{"fatal report", "variant: fcos\nversion: 1.5.0\nstorage:\n  files:\n    - path: {{.JoinString}}\n", http.StatusInternalServerError, "line 5 col 13: path not absolute"},
		// Non-fatal warning whose report entry quotes the value.
		{"warning report quotes the value", "variant: fcos\nversion: 1.5.0\n{{.JoinString}}: x\n", http.StatusOK, "line 3 col 1: unused key"},
	}
	for _, tc := range cases {
		for _, bound := range []bool{false, true} {
			name := tc.name + "/file"
			if bound {
				name = tc.name + "/bound"
			}
			t.Run(name, func(t *testing.T) {
				s := servingStore(t)
				viper.Set(config.JoinString, joinSentinel)
				viper.Set(config.IgnitionFile, "config/ignition.yaml")
				writeFile(t, "config/ignition.yaml", tc.tmpl)

				const mac = "aa:bb:cc:dd:ee:a1"
				if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar", Approved: true}); err != nil {
					t.Fatal(err)
				}
				if bound {
					cid, err := s.CreateConfig("leaky-butane", "butane")
					if err != nil {
						t.Fatal(err)
					}
					rid, _, err := s.AddConfigRevision(cid, base64.StdEncoding.EncodeToString([]byte(tc.tmpl)), "sha", nil)
					if err != nil {
						t.Fatal(err)
					}
					if err = s.SetActiveRevision(cid, rid); err != nil {
						t.Fatal(err)
					}
					if err = hardware.SetHostConfig(mac, &cid); err != nil {
						t.Fatal(err)
					}
				}

				var logBuf bytes.Buffer
				orig := slog.Default()
				slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
				t.Cleanup(func() { slog.SetDefault(orig) })

				rr := httptest.NewRecorder()
				ignitionHandler(s, nil)(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ignition.json?mac="+mac, http.NoBody))

				if rr.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d: %s", rr.Code, tc.wantStatus, rr.Body.String())
				}
				logs := logBuf.String()
				if strings.Contains(logs, joinSentinel) {
					t.Errorf("the join string was logged:\n%s", logs)
				}
				// The bound path logs only renderConfig's error, never the
				// report entries, so the detail is asserted on the file path.
				if !bound && !strings.Contains(logs, tc.wantInLog) {
					t.Errorf("logs lost the operator detail %q:\n%s", tc.wantInLog, logs)
				}
			})
		}
	}
}
