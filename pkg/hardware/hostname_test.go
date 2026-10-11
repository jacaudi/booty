package hardware

import (
	"slices"
	"strings"
	"testing"
)

func TestValidateHostname(t *testing.T) {
	long := strings.Repeat("a", 63)
	tests := []struct {
		name string
		ok   bool
	}{
		{"node1", true},
		{"node-1", true},
		{"Node-1", true},
		{"node1.example.com", true},
		{"a", true},
		{"1", true},
		{long, true},
		{long + "." + long + "." + long + "." + strings.Repeat("a", 61), true},
		{"", false},
		{"a\nb", false},
		{"a\n", false},
		{"\x7fa", false},
		{"-node", false},
		{"node-", false},
		{"node_1", false},
		{"Bad_Name!", false},
		{"node..1", false},
		{".node", false},
		{"node.", false},
		{"node 1", false},
		{long + "a", false},
		{long + "." + long + "." + long + "." + strings.Repeat("a", 62), false},
	}
	for _, tc := range tests {
		err := ValidateHostname(tc.name)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateHostname(%q) err=%v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

func TestStripControl(t *testing.T) {
	tests := []struct{ in, want string }{
		{"node1", "node1"},
		{"a\nb", "ab"},
		{"a\r\n\tb\x00", "ab"},
		{"web_01", "web_01"},
	}
	for _, tc := range tests {
		if got := StripControl(tc.in); got != tc.want {
			t.Errorf("StripControl(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestInvalidHostnames(t *testing.T) {
	setupTempDB(t)
	const mac1, mac2, mac3 = "aa:bb:cc:00:00:01", "aa:bb:cc:00:00:02", "aa:bb:cc:00:00:03"
	for mac, name := range map[string]string{mac1: "good", mac2: "web_01", mac3: "a\nb"} {
		if err := WriteMacAddress(mac, Host{MAC: mac, Hostname: name}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := InvalidHostnames()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{mac2 + `="web_01"`, mac3 + `="a\nb"`}
	if !slices.Equal(got, want) {
		t.Errorf("InvalidHostnames() = %q, want %q", got, want)
	}
}
