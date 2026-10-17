package http

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/hardware"

	"github.com/spf13/viper"
)

func fixedARP(mac string) arpFunc {
	return func(net.IP) (net.HardwareAddr, error) { return net.ParseMAC(mac) }
}

func failingARP(net.IP) (net.HardwareAddr, error) { return nil, errors.New("no reply") }

// seedMember registers mac as a Talos cluster member with an encrypted frozen
// node config and returns the frozen (plaintext) bytes.
func seedMember(t *testing.T, s *db.Store, mac string) []byte {
	t.Helper()
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos", Approved: true}); err != nil {
		t.Fatal(err)
	}
	approveHost(t, mac)
	cid, err := s.CreateCluster("serve", "https://10.0.0.10:6443", "v1.13.5", "v1.34.0", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	frozen := []byte("version: v1alpha1\nmachine:\n  type: controlplane\n# frozen bytes\n")
	enc, err := encryptSecrets(frozen)
	if err != nil {
		t.Fatal(err)
	}
	ncID, _, err := s.AddClusterNodeConfig(mac, cid, enc, "sha", "generated", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = hardware.SetHostCluster(mac, &cid); err != nil {
		t.Fatal(err)
	}
	if err = hardware.SetHostNodeConfig(mac, &ncID); err != nil {
		t.Fatal(err)
	}
	return frozen
}

func TestMemberConfigRequiresMatchingARP(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:80"
	cases := []struct {
		name       string
		arp        arpFunc
		remoteAddr string
		wantCode   int
	}{
		{"arp matches", fixedARP(mac), "10.0.0.50:1234", 200},
		{"arp matches, upper-case reply", fixedARP("AA:BB:CC:DD:EE:80"), "10.0.0.50:1234", 200},
		{"arp names another host", fixedARP("aa:bb:cc:dd:ee:99"), "10.0.0.50:1234", 403},
		{"arp fails", failingARP, "10.0.0.50:1234", 403},
		{"remote addr without port", fixedARP(mac), "10.0.0.50", 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := servingStore(t)
			testSecretsKey(t)
			frozen := seedMember(t, s, mac)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/machineconfig?mac="+mac, http.NoBody)
			req.RemoteAddr = c.remoteAddr
			rr := httptest.NewRecorder()
			machineConfigHandler(s, c.arp, allowProof{})(rr, req)
			if rr.Code != c.wantCode {
				t.Fatalf("code = %d, want %d; body %q", rr.Code, c.wantCode, rr.Body.String())
			}
			if c.wantCode == 200 && rr.Body.String() != string(frozen) {
				t.Fatalf("body not the frozen config")
			}
			if c.wantCode == 403 && strings.Contains(rr.Body.String(), "frozen bytes") {
				t.Fatalf("403 body leaked the config")
			}
		})
	}
}

func TestNonMemberIgnoresARP(t *testing.T) {
	s := servingStore(t)
	viper.Set(config.TalosConfigFile, "config/machineconfig.yaml")
	writeFile(t, "config/machineconfig.yaml", "machine: {}\n")
	const mac = "aa:bb:cc:dd:ee:81"
	if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "talos", Approved: true}); err != nil {
		t.Fatal(err)
	}
	approveHost(t, mac)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/machineconfig?mac="+mac, http.NoBody)
	rr := httptest.NewRecorder()
	machineConfigHandler(s, failingARP, allowProof{})(rr, req)
	if rr.Code != 200 {
		t.Fatalf("non-member must not need ARP: %d %q", rr.Code, rr.Body.String())
	}
}
