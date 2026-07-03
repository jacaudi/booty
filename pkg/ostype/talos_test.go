package ostype

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestTalos_Basics(t *testing.T) {
	o, _ := Lookup("talos")
	if o.Family().Name != "talos" {
		t.Errorf("family = %q, want talos", o.Family().Name)
	}
	if !slices.Equal(o.RequiredParams(), []string{"schematic"}) {
		t.Errorf("RequiredParams = %v, want [schematic]", o.RequiredParams())
	}
	if err := o.ValidateVersion("v1.10.5"); err != nil {
		t.Errorf("valid talos version rejected: %v", err)
	}
	if err := o.ValidateVersion("1.10.5-bad-"); err == nil {
		t.Error("invalid talos version accepted")
	}
	if o.CompareVersions("v1.10.5", "v1.9.0") <= 0 {
		t.Error("v1.10.5 should sort after v1.9.0")
	}
}

func TestTalos_Artifacts(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.TalosFactoryURL, "https://factory.example")
	o, _ := Lookup("talos")
	got, err := o.Artifacts(t.Context(), "v1.10.5", "amd64", map[string]string{"schematic": "abc"})
	if err != nil {
		t.Fatalf("Artifacts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("talos artifacts = %d, want 2", len(got))
	}
	want := "https://factory.example/image/abc/v1.10.5/kernel-amd64"
	if got[0].URL != want {
		t.Errorf("kernel URL = %q, want %q", got[0].URL, want)
	}
}

func TestTalos_DiscoverVersions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/versions" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]string{"v1.9.0", "v1.10.5"})
	}))
	t.Cleanup(srv.Close)

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set(config.TalosFactoryURL, srv.URL)

	o, _ := Lookup("talos")
	got, err := o.DiscoverVersions(t.Context(), nil)
	if err != nil {
		t.Fatalf("DiscoverVersions: %v", err)
	}
	if !slices.Equal(got, []string{"v1.9.0", "v1.10.5"}) {
		t.Errorf("DiscoverVersions = %v, want [v1.9.0 v1.10.5]", got)
	}
}
