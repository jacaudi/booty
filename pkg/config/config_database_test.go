package config

import (
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestDatabasePathValue_DefaultsUnderDataDir(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(DataDir, "/data")

	got := DatabasePathValue()
	want := filepath.Join("/data", "booty.db")
	if got != want {
		t.Errorf("DatabasePathValue() = %q, want %q", got, want)
	}
}

func TestDatabasePathValue_ExplicitWins(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(DataDir, "/data")
	viper.Set(DatabasePath, "/custom/booty.db")

	if got := DatabasePathValue(); got != "/custom/booty.db" {
		t.Errorf("DatabasePathValue() = %q, want /custom/booty.db", got)
	}
}
