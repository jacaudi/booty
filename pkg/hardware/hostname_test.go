package hardware

import (
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
