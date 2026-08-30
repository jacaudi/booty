package http

import (
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func TestInfoIsServedUnderAPIV1(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("version", "v9.9.9")
	viper.Set("timestamp", "2026-08-29 00:00:00")
	viper.Set(config.FlatcarArchitecture, "amd64")
	viper.Set(config.CoreOSArchitecture, "x86_64")

	api := newTestAPI(t, APIDeps{})
	resp := api.Get("/api/v1/info")
	if resp.Code != 200 {
		t.Fatalf("GET /api/v1/info = %d, want 200", resp.Code)
	}
	body := resp.Body.String()
	for _, want := range []string{"v9.9.9", "booty", "flatcar", "coreos"} {
		if !strings.Contains(body, want) {
			t.Errorf("info body missing %q: %s", want, body)
		}
	}
}
