package nodereport

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeCaps(t *testing.T) {
	h := Health{Running: strings.Repeat("r", 300)}
	for range 250 {
		h.FailedUnits = append(h.FailedUnits, "u.service")
	}
	for range 60 {
		h.JournalErrors = append(h.JournalErrors, strings.Repeat("x", 400))
	}
	if err := h.Normalize(); err != nil {
		t.Fatal(err)
	}
	if len(h.FailedUnits) != 200 {
		t.Errorf("failed units = %d, want 200", len(h.FailedUnits))
	}
	if len(h.JournalErrors) > 50 {
		t.Errorf("journal lines = %d, want <= 50", len(h.JournalErrors))
	}
	total := 0
	for _, l := range h.JournalErrors {
		if len(l) > 300 {
			t.Errorf("journal line is %d bytes, want <= 300", len(l))
		}
		total += len(l)
	}
	if total > 8192 {
		t.Errorf("journal total = %d, want <= 8192", total)
	}
	if len(h.Running) > 256 || !strings.HasSuffix(h.Running, "…") {
		t.Errorf("Running = %d bytes, suffix ok=%v", len(h.Running), strings.HasSuffix(h.Running, "…"))
	}
}

func TestNormalizeFirmware(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"UEFI", "uefi", false},
		{"", "", false},
		{"coreboot", "", true},
	}
	for _, tt := range tests {
		h := Health{Firmware: tt.in}
		err := h.Normalize()
		if tt.wantErr {
			if !errors.Is(err, ErrInvalidHealth) {
				t.Errorf("%q: err = %v, want ErrInvalidHealth", tt.in, err)
			}
			continue
		}
		if err != nil || h.Firmware != tt.want {
			t.Errorf("%q: got %q, %v; want %q", tt.in, h.Firmware, err, tt.want)
		}
	}
}

func TestNormalizeDropsEmptyUnits(t *testing.T) {
	h := Health{FailedUnits: []string{"", " ", "a.service"}}
	if err := h.Normalize(); err != nil {
		t.Fatal(err)
	}
	if len(h.FailedUnits) != 1 || h.FailedUnits[0] != "a.service" {
		t.Errorf("FailedUnits = %q", h.FailedUnits)
	}
}

func TestSummary(t *testing.T) {
	h := Health{
		ReceivedAt:  "2026-01-01T00:00:00Z",
		Running:     "1.2.3",
		Kernel:      "6.1",
		Firmware:    FirmwareUEFI,
		FailedUnits: []string{"a", "b"},
		DMI:         DMI{Vendor: "Acme", Product: "Box"},
	}
	s := h.Summary()
	want := Summary{
		Running: "1.2.3", Kernel: "6.1", Firmware: "uefi", ReceivedAt: "2026-01-01T00:00:00Z",
		FailedUnitCount: 2, Vendor: "Acme", Product: "Box",
	}
	if s != want {
		t.Errorf("Summary = %+v, want %+v", s, want)
	}
}
