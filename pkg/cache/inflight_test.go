package cache

import "testing"

func TestIsInFlightPath(t *testing.T) {
	cases := []struct {
		p    string
		want bool
	}{
		{"/cache/flatcar/stable/amd64/1/x.img.partial", true},
		{"/cache/tails/-/amd64/7.10/tails-amd64.iso.download", true},
		{"cache/x.PARTIAL", true},
		{"cache/x.Download", true},
		{"/cache/flatcar/stable/amd64/1/x.img", false},
		{"public/partial.txt", false},
	}
	for _, c := range cases {
		if got := IsInFlightPath(c.p); got != c.want {
			t.Errorf("IsInFlightPath(%q) = %v, want %v", c.p, got, c.want)
		}
	}
}
