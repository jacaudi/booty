package tftp

import (
	"slices"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
)

func TestAllowedDataFile(t *testing.T) {
	top := []string{"undionly.kpxe", "ipxe.efi", "snponly.efi", "ipxe-arm64.efi", "custom-bios.kpxe"}
	cases := []struct {
		name string
		want bool
	}{
		{"cache/flatcar/stable/amd64/1/flatcar_production_pxe.vmlinuz", true},
		{"public/scripts/join.sh", true},
		{"undionly.kpxe", true},
		{"ipxe.efi", true},
		{"snponly.efi", true},
		{"ipxe-arm64.efi", true},
		{"custom-bios.kpxe", true},
		{"api-token", false},
		{"booty.db", false},
		{"booty.db-wal", false},
		{"node-report-key", false},
		{"config/ignition.yaml", false},
		{"cache", false},
		{"cache/", false},
		{"public", false},
		{"public/../api-token", false},
		{"public/../cache/x.img", true},
		{"../api-token", false},
		{"/etc/passwd", false},
		{"cache/flatcar/x.img.partial", false},
		{"cache/tails/x.iso.DOWNLOAD", false},
		{"sub/undionly.kpxe", false},
		{"", false},
	}
	for _, c := range cases {
		if got := allowedDataFile(c.name, top); got != c.want {
			t.Errorf("allowedDataFile(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTopLevelBootFilesIncludesFlagValues(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.ProxyDHCPBootfileBIOS, "my-bios.kpxe")
	viper.Set(config.ProxyDHCPBootfileUEFI, "ipxe.efi")
	viper.Set(config.ProxyDHCPBootfileARM64, "")
	got := topLevelBootFiles()
	want := []string{"ipxe-arm64.efi", "ipxe.efi", "my-bios.kpxe", "snponly.efi", "undionly.kpxe"}
	if !slices.Equal(got, want) {
		t.Fatalf("topLevelBootFiles() = %v, want %v", got, want)
	}
}
