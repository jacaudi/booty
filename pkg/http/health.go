package http

import (
	"encoding/json"
	"net/http"

	"github.com/spf13/viper"
)

// handleHealthz is an unauthenticated liveness probe mounted on the base mux
// (outside the /api/v1 huma group) so a container HEALTHCHECK and the dashboard
// System card can reach it without auth. It touches no DB — it reports that the
// HTTP server is up, plus the build version — so it stays green while a
// background reconcile is busy. The version comes from the "version" viper key
// (bound from the ldflags build version at startup; "dev" when unset), same as
// the other version surfaces.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"version": viper.GetString("version"),
	})
}
