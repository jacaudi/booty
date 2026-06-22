package tftp

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/j-keck/arping"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
	"github.com/pin/tftp"
	"github.com/spf13/viper"
)

// absDataDir is the absolute, cleaned form of viper's DataDir, resolved once
// in StartTFTP. safeJoin reads it; do not mutate after StartTFTP returns.
var absDataDir string

// errPathEscapes is returned by safeJoin when the requested path resolves
// outside absDataDir (e.g. via "..", absolute paths, or sneaky combinations).
var errPathEscapes = errors.New("tftp: path escapes dataDir")

// safeJoin resolves requested against absDataDir and returns an absolute,
// cleaned path under absDataDir, or errPathEscapes if the result would lie
// outside the root.
//
// Note: this does not call filepath.EvalSymlinks. If absDataDir contains a
// symlink whose target is outside the directory, safeJoin will not detect it.
// TFTP is read-only and the operator controls dataDir contents; this is an
// acceptable limitation.
func safeJoin(requested string) (string, error) {
	if absDataDir == "" {
		return "", errors.New("tftp: absDataDir not initialized")
	}
	// Reject absolute-path requests as a security policy: TFTP clients must
	// not be able to address files by absolute path, even though
	// filepath.Join would in practice keep the result under absDataDir
	// (Join("/dataDir", "/etc/passwd") returns "/dataDir/etc/passwd").
	if filepath.IsAbs(requested) {
		return "", errPathEscapes
	}
	joined := filepath.Join(absDataDir, requested)
	cleaned := filepath.Clean(joined)
	if cleaned != absDataDir &&
		!strings.HasPrefix(cleaned, absDataDir+string(filepath.Separator)) {
		return "", errPathEscapes
	}
	return cleaned, nil
}

// readHandler is called when client starts file download from server
func readHandler(filename string, rf io.ReaderFrom) error {
	slog.Info("TFTP get", "file", filename)
	raddr := rf.(tftp.OutgoingTransfer).RemoteAddr()
	laddr := rf.(tftp.RequestPacketInfo).LocalIP()
	slog.Debug("RRQ", "from", raddr.String(), "to", laddr.String())

	osToLoad := "flatcar"
	menuDefault := "run-from-disk"

	if hwAddr, _, err := arping.Ping(raddr.IP); err != nil {
		slog.Warn("error with ARP request", "err", err)
	} else {
		macAddress := hwAddr.String()
		host, lookupErr := hardware.GetMacAddress(macAddress)
		if lookupErr != nil && !errors.Is(lookupErr, hardware.ErrNotFound) {
			slog.Warn("TFTP: error looking up host", "mac", macAddress, "err", lookupErr)
		}
		if host != nil {
			if host.OS != "" {
				osToLoad = host.OS
			}
			if host.DoInstall {
				menuDefault = "install"
				if filename == "booty.ipxe" {
					modified := *host
					modified.DoInstall = false
					if err := hardware.WriteMacAddress(macAddress, modified); err != nil {
						slog.Warn("TFTP: error persisting DoInstall flip", "mac", macAddress, "err", err)
						// Best-effort: continue serving the iPXE script even if
						// the persist failed; the next boot will retry.
					}
				}
			}
		}
	}

	urlHost := viper.GetString(config.ServerIP)
	hostPort := viper.GetInt(config.ServerHttpPort)
	if hostPort != 80 {
		urlHost = fmt.Sprintf("%s:%d", urlHost, hostPort)
	}

	if filename == "booty.ipxe" {
		toServe := strings.Replace(PXEConfig[fmt.Sprintf("%s.ipxe", osToLoad)], "[[server]]", urlHost, -1)
		toServe = strings.Replace(toServe, "[[menu-default]]", menuDefault, -1)
		toServe = strings.Replace(toServe, "[[coreos-channel]]", viper.GetString(config.CoreOSChannel), -1)
		toServe = strings.Replace(toServe, "[[coreos-arch]]", viper.GetString(config.CoreOSArchitecture), -1)
		toServe = strings.Replace(toServe, "[[coreos-version]]", viper.GetString(config.CurrentCoreOSVersion), -1)

		r := strings.NewReader(toServe)
		n, err := rf.ReadFrom(r)
		if err != nil {
			slog.Warn("error reading iPXE config", "err", err)
			return err
		}
		slog.Info("bytes sent", "bytes", n, "file", filename)
		return nil
	}

	if filename == "pxelinux.cfg/default" {
		r := strings.NewReader(strings.Replace(PXEConfig[osToLoad], "[[server]]", urlHost, -1))
		n, err := rf.ReadFrom(r)
		if err != nil {
			slog.Warn("error reading PXE config", "err", err)
			return err
		}
		slog.Info("bytes sent", "bytes", n, "file", filename)
		return nil
	}
	path, err := safeJoin(filename)
	if err != nil {
		if errors.Is(err, errPathEscapes) {
			slog.Warn("TFTP rejected: path escapes dataDir", "client", raddr.String(), "requested", filename)
		}
		return os.ErrNotExist
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	n, err := rf.ReadFrom(file)
	if err != nil {
		return err
	}
	slog.Info("bytes sent", "bytes", n, "file", filename)
	return nil
}

// writeHandler is called when client starts file upload to server
func writeHandler(filename string, wt io.WriterTo) error {
	slog.Info("TFTP writes are not supported", "file", filename)
	return nil
}

// StartTFTP starts the TFTP server in a background goroutine and returns it so
// the caller can Shutdown() it during graceful shutdown. The returned server's
// Shutdown drains outstanding transfers before stopping the listener.
func StartTFTP() *tftp.Server {
	resolved, err := filepath.Abs(viper.GetString(config.DataDir))
	if err != nil {
		slog.Error("TFTP: failed to resolve dataDir", "err", err)
		os.Exit(1)
	}
	absDataDir = resolved

	// use nil in place of handler to disable read or write operations
	s := tftp.NewServer(readHandler, writeHandler)
	s.SetBlockSize(512)
	s.EnableSinglePort()
	s.SetTimeout(60 * time.Second) // optional
	go func() {
		err := s.ListenAndServe(":69") // blocks until s.Shutdown() is called
		if err != nil {
			slog.Error("TFTP server error", "err", err)
			os.Exit(1)
		}
	}()
	slog.Info("TFTP server started")
	return s
}
