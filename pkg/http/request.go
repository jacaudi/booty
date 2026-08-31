package http

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/spf13/viper"
)

func handleRequest(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/", http.StatusFound)
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
