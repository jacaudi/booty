package http

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeefy/booty/pkg/hardware"
)

// TestHoldingScriptHasNoTicket: a machine booty will not serve gets the
// holding script, which carries no ticket, so it can never fetch a config.
func TestHoldingScriptHasNoTicket(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:54"
	cases := []struct {
		name                      string
		register, approve, revoke bool
	}{
		{"unknown MAC", false, false, false},
		{"unapproved host", true, false, false},
		{"revoked host", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := servingStore(t)
			if c.register {
				if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar"}); err != nil {
					t.Fatal(err)
				}
			}
			if c.approve {
				approveHost(t, mac)
				if err := hardware.SetAssignment(mac, "flatcar", "", ""); err != nil {
					t.Fatal(err)
				}
			}
			if c.revoke {
				if err := hardware.Revoke(mac); err != nil {
					t.Fatal(err)
				}
			}
			body := fetchConfig(t, bootScriptHandler(s, failingARP, testTicketer(t)), "/booty.ipxe?mac="+mac).Body.String()
			if !strings.Contains(body, "not yet approved") {
				t.Fatalf("want the holding script, got:\n%s", body)
			}
			if strings.Contains(body, "ticket=") {
				t.Fatalf("holding script carries a ticket:\n%s", body)
			}
		})
	}
	t.Run("control: approved assigned host", func(t *testing.T) {
		s := servingStore(t)
		if err := hardware.WriteMacAddress(mac, hardware.Host{MAC: mac, OS: "flatcar"}); err != nil {
			t.Fatal(err)
		}
		approveHost(t, mac)
		if err := hardware.SetAssignment(mac, "flatcar", "", ""); err != nil {
			t.Fatal(err)
		}
		body := fetchConfig(t, bootScriptHandler(s, failingARP, testTicketer(t)), "/booty.ipxe?mac="+mac).Body.String()
		if !strings.Contains(body, "ticket=v1.") {
			t.Fatalf("an approved assigned host's script must carry a ticket:\n%s", body)
		}
	})
}

// TestStartHTTPRefusesNilTickets: without an issuer no boot script carries a
// ticket and every config route would refuse, so StartHTTP will not start,
// with or without --noAuth.
func TestStartHTTPRefusesNilTickets(t *testing.T) {
	for _, c := range []struct {
		name string
		deps APIDeps
	}{
		{"with auth", APIDeps{Auth: tokenStore(t, "tok")}},
		{"under noAuth", APIDeps{NoAuth: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, err := StartHTTP(c.deps, make(chan error, 1))
			if err == nil {
				if srv != nil {
					_ = srv.Close()
				}
				t.Fatal("StartHTTP with nil Tickets = nil error, want a refusal")
			}
			if !errors.Is(err, errNoTicketIssuer) {
				t.Errorf("StartHTTP error = %v, want errNoTicketIssuer", err)
			}
		})
	}
}
