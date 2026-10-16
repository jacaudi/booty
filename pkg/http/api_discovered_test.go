package http

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/hardware"
)

func TestCreateHostStoresBootVocabulary(t *testing.T) {
	s := servingStore(t)
	api := newTestAPI(t, APIDeps{Store: s})
	const mac = "aa:bb:cc:dd:ee:10"

	resp := api.Post("/api/v1/hosts", map[string]any{"mac": mac, "os": "fedora-coreos"})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", resp.Code, resp.Body.String())
	}
	h, err := hardware.GetMacAddress(mac)
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if h.OS != "coreos" {
		t.Errorf("stored OS = %q, want %q", h.OS, "coreos")
	}
}

func TestApproveRefusesToolOS(t *testing.T) {
	s := servingStore(t)
	api := newTestAPI(t, APIDeps{Store: s})
	const mac = "aa:bb:cc:dd:ee:11"

	resp := api.Post("/api/v1/hosts", map[string]any{"mac": mac, "os": "memtest86plus"})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", resp.Code, resp.Body.String())
	}
	resp = api.Post("/api/v1/hosts/"+mac+"/approve", map[string]any{})
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("approve = %d, want 422: %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "cannot be booted by assignment") {
		t.Errorf("422 body = %s, want it to say %q", resp.Body.String(), "cannot be booted by assignment")
	}
	h, err := hardware.GetMacAddress(mac)
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	if h.Approved {
		t.Error("a refused approve must leave the host unapproved")
	}
}

func TestApproveAcceptsAssignableOS(t *testing.T) {
	tests := []struct {
		os  string
		mac string
	}{
		{"talos", "aa:bb:cc:dd:ee:21"},
		{"flatcar", "aa:bb:cc:dd:ee:22"},
		{"coreos", "aa:bb:cc:dd:ee:23"},
		{"debian", "aa:bb:cc:dd:ee:24"},
	}
	s := servingStore(t)
	api := newTestAPI(t, APIDeps{Store: s})
	for _, tt := range tests {
		t.Run(tt.os, func(t *testing.T) {
			resp := api.Post("/api/v1/hosts", map[string]any{"mac": tt.mac, "os": tt.os})
			if resp.Code != http.StatusCreated {
				t.Fatalf("create = %d, want 201: %s", resp.Code, resp.Body.String())
			}
			resp = api.Post("/api/v1/hosts/"+tt.mac+"/approve", map[string]any{})
			if resp.Code != http.StatusOK {
				t.Fatalf("approve = %d, want 200: %s", resp.Code, resp.Body.String())
			}
			h, err := hardware.GetMacAddress(tt.mac)
			if err != nil {
				t.Fatalf("get host: %v", err)
			}
			if !h.Approved {
				t.Error("host is not approved")
			}
		})
	}
}

func TestApproveEmptyOSStillWorks(t *testing.T) {
	s := servingStore(t)
	api := newTestAPI(t, APIDeps{Store: s})
	const mac = "aa:bb:cc:dd:ee:25"

	resp := api.Post("/api/v1/hosts", map[string]any{"mac": mac})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", resp.Code, resp.Body.String())
	}
	resp = api.Post("/api/v1/hosts/"+mac+"/approve", map[string]any{})
	if resp.Code != http.StatusOK {
		t.Fatalf("approve = %d, want 200: %s", resp.Code, resp.Body.String())
	}
}

func TestDismissDiscovered(t *testing.T) {
	s := servingStore(t)
	api := newTestAPI(t, APIDeps{Store: s})
	const mac = "aa:bb:cc:dd:ee:13"
	if _, err := s.InsertDiscoveredHost(t.Context(), mac, "10.0.0.7", "amd64", maxDiscoveredHosts); err != nil {
		t.Fatal(err)
	}

	if resp := api.Delete("/api/v1/discovered/" + mac); resp.Code != http.StatusNoContent {
		t.Fatalf("dismiss = %d, want 204: %s", resp.Code, resp.Body.String())
	}
	n, err := s.CountDiscoveredHosts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("discovered count = %d, want 0", n)
	}
	if resp := api.Delete("/api/v1/discovered/" + mac); resp.Code != http.StatusNotFound {
		t.Errorf("repeat dismiss = %d, want 404: %s", resp.Code, resp.Body.String())
	}
	if resp := api.Delete("/api/v1/discovered/zz"); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("dismiss zz = %d, want 422: %s", resp.Code, resp.Body.String())
	}
}

// TestDismissDiscoveredLeavesHostsRow: dismissing a MAC that is also a
// registered host deletes only the discovered row. The hosts row is inserted
// with raw SQL because UpsertHost would delete the discovered row itself.
func TestDismissDiscoveredLeavesHostsRow(t *testing.T) {
	s := servingStore(t)
	api := newTestAPI(t, APIDeps{Store: s})
	const mac = "aa:bb:cc:dd:ee:12"
	if _, err := s.InsertDiscoveredHost(t.Context(), mac, "10.0.0.7", "amd64", maxDiscoveredHosts); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(viper.GetString(config.DataDir), "booty.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.ExecContext(t.Context(), "INSERT INTO hosts (mac) VALUES (?)", mac)
	if cerr := raw.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatal(err)
	}

	if resp := api.Delete("/api/v1/discovered/" + mac); resp.Code != http.StatusNoContent {
		t.Fatalf("dismiss = %d, want 204: %s", resp.Code, resp.Body.String())
	}
	exists, err := hardware.HasHost(mac)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("dismissing a discovered row deleted the hosts row")
	}
}

func TestDismissAllDiscovered(t *testing.T) {
	s := servingStore(t)
	api := newTestAPI(t, APIDeps{Store: s})
	for i := range 3 {
		mac := fmt.Sprintf("aa:bb:cc:dd:ee:3%d", i)
		if _, err := s.InsertDiscoveredHost(t.Context(), mac, "10.0.0.7", "amd64", maxDiscoveredHosts); err != nil {
			t.Fatal(err)
		}
	}

	resp := api.Delete("/api/v1/discovered")
	if resp.Code != http.StatusOK {
		t.Fatalf("dismiss all = %d, want 200: %s", resp.Code, resp.Body.String())
	}
	var got struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Deleted != 3 {
		t.Errorf("deleted = %d, want 3", got.Deleted)
	}
	n, err := s.CountDiscoveredHosts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("discovered count = %d, want 0", n)
	}
}
