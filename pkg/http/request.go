package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func handleRequest(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/", http.StatusFound)
}

func handleDataRequest(w http.ResponseWriter, r *http.Request) {
	data, err := hardware.GetData()
	if err != nil {
		slog.Error("error getting hardware data", "err", err)
		http.Error(w, "Error getting hardware data", http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

func handleHostsRequest(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	if mac == "" {
		http.Error(w, "MAC address is required", http.StatusBadRequest)
		return
	}

	host, err := hardware.GetMacAddress(mac)
	if errors.Is(err, hardware.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("error looking up host", "mac", mac, "err", err)
		http.Error(w, "Error retrieving host", http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(host)
	if err != nil {
		slog.Error("error marshalling host", "mac", mac, "err", err)
		http.Error(w, "Error marshalling host data", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func handleVersionRequest(w http.ResponseWriter, r *http.Request) {
	flatcar := cache.NewestCached("flatcar", viper.GetString(config.FlatcarArchitecture), nil)
	coreos := cache.NewestCached("coreos", viper.GetString(config.CoreOSArchitecture), nil)
	if strings.Contains(r.RequestURI, "json") {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"flatcar":"%s","coreos":"%s"}`, flatcar, coreos)))
		return
	}
	w.Write([]byte(fmt.Sprintf("FLATCAR_VERSION=%s\n", flatcar)))
	w.Write([]byte(fmt.Sprintf("COREOS_VERSION=%s\n", coreos)))
}

func handleInfoRequest(w http.ResponseWriter, r *http.Request) {
	flatcar := cache.NewestCached("flatcar", viper.GetString(config.FlatcarArchitecture), nil)
	coreos := cache.NewestCached("coreos", viper.GetString(config.CoreOSArchitecture), nil)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprintf(`{"flatcar":{"version":"%s"},"coreos":{"version":"%s"},"booty":{"version":"%s","timestamp":"%s"}}`,
		flatcar, coreos, viper.GetString("version"), viper.GetString("timestamp"))))
}
