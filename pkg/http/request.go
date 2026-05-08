package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

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
		log.Printf("Error getting hardware data: %s", err.Error())
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
		log.Printf("Error looking up host %s: %s", mac, err.Error())
		http.Error(w, "Error retrieving host", http.StatusInternalServerError)
		return
	}

	data, err := json.Marshal(host)
	if err != nil {
		log.Printf("Error marshalling host %s: %s", mac, err.Error())
		http.Error(w, "Error marshalling host data", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func handleVersionRequest(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.RequestURI, "json") {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(`{"flatcar":"%s","coreos":"%s"}`, viper.GetString(config.CurrentFlatcarVersion), viper.GetString(config.CurrentCoreOSVersion))))
		return
	}
	w.Write([]byte(fmt.Sprintf("FLATCAR_VERSION=%s\n", viper.GetString(config.CurrentFlatcarVersion))))
	w.Write([]byte(fmt.Sprintf("COREOS_VERSION=%s\n", viper.GetString(config.CurrentCoreOSVersion))))
}

func handleInfoRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(fmt.Sprintf(`{"flatcar":{"version":"%s"},"coreos":{"version":"%s"},"booty":{"version":"%s","timestamp":"%s"}}`, viper.GetString(config.CurrentFlatcarVersion), viper.GetString(config.CurrentCoreOSVersion), viper.GetString("version"), viper.GetString("timestamp"))))
}
