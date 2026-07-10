package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/jeefy/booty/pkg/cache"
	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
)

// factoryBuildTimeout bounds the Image Factory schematic-build POST (SGE M1):
// a short per-request context deadline — NOT config.httpClient's 5-minute
// download ceiling — so a slow/unreachable Factory fails the create/update
// request quickly (surfaced as 422 by the caller) instead of hanging it for
// minutes.
const factoryBuildTimeout = 15 * time.Second

// factoryHTTPClient is a package-level client dedicated to Factory calls.
// It intentionally carries no client.Timeout: factoryBuildTimeout (composed
// with any caller deadline via context.WithTimeoutCause in buildSchematic)
// is the sole bound, per M1. A bare *http.Client{} — not http.DefaultClient
// — mirrors the existing pkg/config.httpClient convention for outbound
// calls in this codebase (go-standards.md §1: match established patterns)
// and avoids relying on the shared package-global DefaultClient, which
// go-standards.md §15.1 flags as unsafe for production use.
var factoryHTTPClient = &http.Client{}

// buildSchematic submits a customization source (YAML) to the Image Factory
// (POST <factoryURL>/schematics) and returns the content-addressed schematic
// ID the Factory assigned. The Factory owns the build; booty performs one
// bounded stdlib POST and records the result (design D2 — no vendored
// client). Air-gap deployments point --talosFactoryURL at a private Factory;
// this function has no other knob. Any transport error, non-2xx status, or
// unusable ID is returned as an error for the caller to surface as 422.
func buildSchematic(ctx context.Context, factoryURL string, source []byte) (string, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, factoryBuildTimeout,
		errors.New("http: image factory build timed out"))
	defer cancel()

	schematicsURL, err := url.JoinPath(factoryURL, "schematics")
	if err != nil {
		return "", fmt.Errorf("http: build schematic url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, schematicsURL, bytes.NewReader(source))
	if err != nil {
		return "", fmt.Errorf("http: build schematic request: %w", err)
	}
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := factoryHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: post schematic to factory: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10)) // bound the response read
	if err != nil {
		return "", fmt.Errorf("http: read factory response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http: factory returned %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("http: decode factory response: %w", err)
	}
	// The ID becomes a cache path segment (<os>/<schematic>/<arch>/<version>)
	// and a factory URL segment — reject anything not path-safe before it can
	// be stored, seeded, or bound (single knowledge site: ValidatePathParam;
	// also rejects an empty ID).
	if err := cache.ValidatePathParam(out.ID); err != nil {
		return "", fmt.Errorf("http: factory returned unusable schematic id: %w", err)
	}
	return out.ID, nil
}

// vanillaSchematicSource is the customization that produces the Factory's
// vanilla (no-extensions) schematic. Because schematics are content-addressed,
// this source's ID is the known constant config.DefaultTalosSchematic — no
// build is needed to record it.
const vanillaSchematicSource = "customization: {}\n"

// SeedVanillaSchematic inserts the baseline "vanilla" schematic config at
// startup, create-if-absent by name (design D7), so the UI catalog always
// shows the Factory's vanilla image. It seeds the KNOWN constant ID directly
// and NEVER POSTs to the Factory (SGE I4): building at startup would couple
// boot to Factory reachability — a disposability regression and an air-gap
// hazard (a private/self-hosted Factory may be down at boot). Idempotent:
// any existing config named "vanilla" makes it a no-op. Its cache target
// needs no ensuring here — seedTargets already seeds the predefined Talos
// target from the --talosSchematic default every reconcile tick.
func SeedVanillaSchematic(store *db.Store) error {
	list, err := store.ListConfigs()
	if err != nil {
		return fmt.Errorf("http: seed vanilla schematic: %w", err)
	}
	if slices.ContainsFunc(list, func(r db.ConfigListRow) bool { return r.Name == "vanilla" }) {
		return nil // already seeded (or operator-owned name): no-op
	}
	vanillaID := config.DefaultTalosSchematic
	// Defense-in-depth: honor the plan-wide "ValidatePathParam applies to every
	// ID before it is stored, seeded, or bound" invariant even for the
	// compile-time-known trusted constant — a bad edit to DefaultTalosSchematic
	// fails fast at startup rather than seeding an unusable cache segment.
	// Validated BEFORE the config row is created: if this ever fails, startup
	// aborts with no row persisted, so a later restart still attempts (and can
	// succeed) seeding once the constant is fixed — rather than leaving behind
	// an orphaned "vanilla" config with no active revision that the presence
	// check above would then permanently skip.
	if err := cache.ValidatePathParam(vanillaID); err != nil {
		return fmt.Errorf("http: seed vanilla schematic: invalid constant id: %w", err)
	}
	id, err := store.CreateConfig("vanilla", "schematic")
	if err != nil {
		return fmt.Errorf("http: seed vanilla schematic: %w", err)
	}
	if err := appendActiveRevision(store, id, vanillaSchematicSource, &vanillaID); err != nil {
		return fmt.Errorf("http: seed vanilla schematic revision: %w", err)
	}
	slog.Info("seeded vanilla schematic config", "schematic", vanillaID)
	return nil
}
