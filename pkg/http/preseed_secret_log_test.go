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

// TestPreseedFailureNeverLogsTheJoinString drives /preseed for a host bound to
// a debianconfig that renders .JoinString and then fails translation. The
// translator quotes template values in its errors, and writeError logs the
// error at ERROR, so the join string (a secret, --joinString) must appear in
// no log record while the operator-facing detail still does.
func TestPreseedFailureNeverLogsTheJoinString(t *testing.T) {
	cases := []struct {
		name      string
		tmpl      string
		wantInLog string // operator-facing detail that must survive redaction
	}{
		// buildPreseedView error that quotes the whole value.
		{"invalid sudo quotes the value", "accounts:\n  user:\n    sudo: {{.JoinString}}\n", "invalid accounts.user.sudo"},
		// A second validator that quotes its value, on a different field.
		{"invalid disk layout quotes the value", "disk:\n  devices: [/dev/sda]\n  layout: {{.JoinString}}\n", "invalid disk.layout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := servingStore(t)
			viper.Set(config.JoinString, joinSentinel)

			const mac = "aa:bb:cc:dd:ee:b1"
			if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "debian", Approved: true}); err != nil {
				t.Fatal(err)
			}
			cid, err := s.CreateConfig("leaky-debianconfig", kindDebianConfig)
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

			var logBuf bytes.Buffer
			orig := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(orig) })

			rr := httptest.NewRecorder()
			handlePreseedRequest(s)(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/preseed?mac="+mac, http.NoBody))

			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusInternalServerError, rr.Body.String())
			}
			logs := logBuf.String()
			if strings.Contains(logs, joinSentinel) {
				t.Errorf("the join string was logged:\n%s", logs)
			}
			if !strings.Contains(logs, tc.wantInLog) {
				t.Errorf("logs lost the operator detail %q:\n%s", tc.wantInLog, logs)
			}
		})
	}
}
