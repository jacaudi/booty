package nodereport

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

func TestDebianLateCommand(t *testing.T) {
	script := "#!/bin/bash\necho 'quoted' \"x\"\n"
	unit := "[Unit]\nDescription=it's\n"
	got := DebianLateCommand(script, unit)
	if !strings.HasPrefix(got, "in-target sh -c '") {
		t.Fatalf("prefix: %s", got)
	}
	for _, want := range []string{
		"base64 -d > /opt/booty/health-report",
		"chmod 0755 /opt/booty/health-report",
		"base64 -d > /etc/systemd/system/booty-health.service",
		"systemctl enable booty-health.service",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	m := regexp.MustCompile(`echo ([A-Za-z0-9+/=]+) \| base64 -d`).FindAllStringSubmatch(got, -1)
	if len(m) != 2 {
		t.Fatalf("payloads = %d", len(m))
	}
	for i, want := range []string{script, unit} {
		b, err := base64.StdEncoding.DecodeString(m[i][1])
		if err != nil || string(b) != want {
			t.Errorf("payload %d: %v %q", i, err, b)
		}
	}
	if n := strings.Count(got, "'"); n != 2 {
		t.Errorf("single quotes = %d, want 2", n)
	}
}
