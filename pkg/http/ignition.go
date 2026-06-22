package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"text/template"

	butaneConfig "github.com/coreos/butane/config"
	butaneCommon "github.com/coreos/butane/config/common"
	coreOSType "github.com/coreos/ignition/v2/config/v3_5_experimental/types"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/spf13/viper"
)

func handleIgnitionRequest(w http.ResponseWriter, r *http.Request) {
	// If we don't identify the host, tell FlatCar to reboot
	// Reboot the host till we identify it
	// Cool so, we want to have logic based around a recognized MAC address
	// Therefore what we need to do is collect the MAC address

	slog.Info("ignition request", "uri", r.RequestURI)

	macAddress := ""

	if r.URL.Query().Get("mac") == "" {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			slog.Warn("error splitting user ip; not IP:port", "remote", r.RemoteAddr)
		}
		remoteIP := net.ParseIP(ip)

		if hwAddr, _, err := arping.Ping(remoteIP); err != nil {
			slog.Warn("error with ARP request", "err", err)
		} else {
			slog.Debug("mac address from ARP", "mac", macAddress)
			macAddress = hwAddr.String()
		}
	} else {
		macAddress = r.URL.Query().Get("mac")
		slog.Debug("mac address url override", "mac", macAddress)
	}

	slog.Debug("using mac address", "mac", macAddress)
	host, err := hardware.GetMacAddress(macAddress)
	if err != nil && !errors.Is(err, hardware.ErrNotFound) {
		slog.Warn("error looking up host", "mac", macAddress, "err", err)
		// Treat unexpected errors the same as a miss — fall through to the
		// reboot-config path so the machine doesn't boot loop on a bad DB.
	}

	var tpl bytes.Buffer

	templateData := struct {
		JoinString  string
		ServerIP    string
		OSTreeImage string
		Hostname    string
	}{
		JoinString: viper.GetString(config.JoinString),
		ServerIP:   fmt.Sprintf("%s:%s", viper.GetString(config.ServerIP), viper.GetString(config.ServerHttpPort)),
	}

	ignitionFile := viper.GetString(config.IgnitionFile)
	if host != nil {
		if host.IgnitionFile != "" {
			ignitionFile = host.IgnitionFile
		}
		templateData.Hostname = host.Hostname

		// Ingelligently rewrite what image to send to the client depending on cache state
		// First, default to the remote image location
		templateData.OSTreeImage = host.OSTreeImage

		// If we have a local image, use that instead
		if host.OSTreeImage != "" {
			localImage := fmt.Sprintf("%s:%s/%s", viper.GetString(config.ServerIP), viper.GetString(config.HttpPort), host.OSTreeImage)
			digest, err := crane.Digest(localImage)
			if err != nil {
				slog.Warn("error getting image from cache", "image", localImage, "err", err)
			}
			if digest == "" {
				slog.Warn("image not found in local cache yet", "image", localImage)
			} else {
				templateData.OSTreeImage = localImage
			}
		}
	}
	t, err := template.ParseFiles(fmt.Sprintf("%s/%s", viper.GetString(config.DataDir), ignitionFile))
	if err != nil {
		w.Write([]byte(err.Error()))
		return
	}

	err = t.Execute(&tpl, templateData)
	if err != nil {
		w.Write([]byte(err.Error()))
		return
	}

	if host == nil {
		coreosConfig := coreOSType.Config{}
		coreosConfig.Ignition.Version = "3.4.0"
		truePointer := true
		contentsPointer := `
[Service]
Type=simple
ExecStart=reboot

[Install]
WantedBy=default.target
`
		coreosConfig.Systemd.Units = append(coreosConfig.Systemd.Units, coreOSType.Unit{
			Name:     "Reboot now please",
			Enabled:  &truePointer,
			Contents: &contentsPointer,
		})
		var dataOut []byte
		dataOut, err := json.Marshal(&coreosConfig)
		if err != nil {
			w.Write([]byte(fmt.Sprintf("Failed to marshal output: %v", err)))
			return
		}
		w.Write(dataOut)
		return
	}

	ignCfg, report, err := butaneConfig.TranslateBytes(tpl.Bytes(), butaneCommon.TranslateBytesOptions{
		Pretty: true,
	})
	if err != nil {
		errMsg := fmt.Sprintf("Error parsing coreos ignition: %s", err.Error())
		slog.Error("error parsing coreos ignition", "err", err)
		slog.Error("rendered ignition template", "template", tpl.String())
		for _, entry := range report.Entries {
			slog.Error("ignition report entry", "entry", entry.String())
		}
		w.Write([]byte(errMsg))
		return
	}
	if len(report.Entries) > 0 {
		errMsg := fmt.Sprintf("Problems parsing coreos ignition: %s", report.String())
		slog.Warn("problems parsing coreos ignition", "report", report.String())
		slog.Warn("rendered ignition template", "template", tpl.String())
		for _, entry := range report.Entries {
			slog.Warn("ignition report entry", "entry", entry.String())
		}
		if report.IsFatal() {
			w.Write([]byte(errMsg))
			return

		}
	}

	w.Write(ignCfg)
}
