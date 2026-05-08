package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func TestLoadConfig_VersionFile(t *testing.T) {
	cases := []struct {
		name        string
		writeFile   bool
		fileContent string
		wantVersion string // empty => CurrentFlatcarVersion should be unset/empty
	}{
		{
			name:        "present with key",
			writeFile:   true,
			fileContent: "FLATCAR_VERSION=1.2.3\n",
			wantVersion: "1.2.3",
		},
		{
			name:        "present without key",
			writeFile:   true,
			fileContent: "OTHER_KEY=value\n",
			wantVersion: "",
		},
		{
			name:        "malformed file",
			writeFile:   true,
			fileContent: "this is not = valid = dotenv = at all\x00",
			wantVersion: "",
		},
		{
			name:        "absent file",
			writeFile:   false,
			wantVersion: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			viper.Reset()
			dir := t.TempDir()
			viper.Set(DataDir, dir)

			if tc.writeFile {
				path := filepath.Join(dir, "version.txt")
				if err := os.WriteFile(path, []byte(tc.fileContent), 0o644); err != nil {
					t.Fatalf("seed file: %v", err)
				}
			}

			LoadConfig(&cobra.Command{})

			got := viper.GetString(CurrentFlatcarVersion)
			if got != tc.wantVersion {
				t.Errorf("CurrentFlatcarVersion = %q, want %q", got, tc.wantVersion)
			}
		})
	}
}
