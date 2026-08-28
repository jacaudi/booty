package cache

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/jeefy/booty/pkg/ostype"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
)

// verifyRetryAfter bounds how often a version REJECTED BY VERIFICATION is
// re-downloaded. Without it, D4a turns a persistent checksum mismatch into a
// 1.94 GB re-pull every --cacheInterval forever: rejection runs
// removeVersionDir, which RemoveAlls the directory including the in-progress
// file, so the next tick starts from zero.
//
// TWO readers, one window. The version loop below reads it via
// db.VerifyRejectedWithin; ensureDebianDVD reads it via dvdVerifyGuarded
// (pkg/cache/debiandvd.go), because Debian DVD targets are dispatched before
// that loop and record no cache_entries row to key a SQL predicate on (#77).
// "How long a verification rejection suppresses a re-download" is one piece of
// knowledge, so it stays single-sourced here rather than being copied.
//
// A package var, not a viper key: both consumers and their tests are
// `package cache`, so the repo's "network dependencies must be viper-backed"
// rule — which is about a dependency read from ANOTHER package — does not
// apply, and ostype's discoveryTimeout is the cheaper local precedent. Promote
// it to a flag when an operator actually needs to tune it, not before.
var verifyRetryAfter = time.Hour

// artifactOutcome keeps one artifact's landArtifact result intact so the
// version-level disposition can tell a REFUSAL from a FAILURE TO EVALUATE. The
// two parallel slices it replaces could not: a refusal and a transport error
// both leave the landed flag false, so the errgroup's single first-error was the
// only thing separating them — and that carries no per-artifact attribution.
type artifactOutcome struct {
	landed  bool
	verdict artifactVerdict
	err     error
}

// rejected reports that landArtifact's disposition switch REFUSED these bytes,
// as opposed to landArtifact having been unable to evaluate them at all. Its
// reject() closure is the only producer of (landed=false, err=nil); every other
// early return in landArtifact carries a non-nil error.
//
// This is NOT the same as "an integrity verdict was reached": verifyArtifact
// classifies some infrastructure faults as classCorruption/classForgery before
// landArtifact ever sees them (verifyDetachedGPG's fetchBytes, os.Open and
// default: arms), and those refusals are indistinguishable here by
// construction. That residual is stated in the design's section 8 and is
// deliberately not fixed here.
//
// Deliberately NOT keyed on the verdict class: a non-Large artifact failing its
// checksum under `warn` LANDS while carrying classCorruption, so a class-keyed
// predicate would reject the version and delete the availability trade-off warn
// exists for.
func (o artifactOutcome) rejected() bool { return o.err == nil && !o.landed }

// reconcileTarget brings ONE target's cache into its desired state. It is called
// only from the reconcile coordinator goroutine, so every DB write here is
// single-threaded (no viper/db races). Failures are non-fatal: a discovery
// fetch error keeps the existing cached set (no prune); a per-artifact download
// error is logged and retried next tick UNLESS another artifact in the same
// version was refused, in which case the refusal is recorded and the version is
// rejected.
//
// Desired set: discovery mode -> DiscoverVersions -> retentionFor, plus any
// existing manual rows; manual mode -> just the existing manual rows. discovered
// rows outside the retained set are pruned (row + dir); manual rows are NEVER
// pruned.
//
// concurrency bounds artifact downloads. The per-version errgroup.Group is used
// purely as a bounded waiter — its goroutines always return nil, because a
// transport error is carried per-artifact in artifactOutcome.err so it can be
// attributed. The coordinator runs targets sequentially, so a per-version cap is
// functionally identical to a global cap at booty's ~3 upstreams.
func reconcileTarget(ctx context.Context, store *db.Store, concurrency int, t db.Target) error {
	// D17: fetch the FCOS channel streams doc at most once per pass; reset the
	// memo at pass entry so a later pass resolves new builds against a fresh doc.
	// #73: the netboot.xyz manifest memo is reset once per PASS in reconcileAll,
	// not here — this function runs once per TARGET, and resetting it here
	// re-fetched the ~35KB manifest once per tool target instead of once per tick.
	ostype.ResetStreamsCache()

	o, ok := ostype.Lookup(t.OS) // t.OS is the canonical taxonomy name
	if !ok {
		return fmt.Errorf("cache: unknown OS %q for target %d", t.OS, t.ID)
	}
	params, err := decodeParams(t.Params)
	if err != nil {
		return fmt.Errorf("cache: target %d params: %w", t.ID, err)
	}

	// Debian DVD-wanted targets take a wholly separate path (download+verify+
	// extract a DVD set, then flip source_mode) and must never fall through to
	// the generic netinst Artifacts path below (which would cache throwaway
	// linux/initrd files for a target that's meant to serve the DVD tree).
	if t.OS == "debian" && wantsDVD(t) {
		// A settled DVD target resolves its version from disk/DB (no network,
		// no re-resolve against upstream every tick) — this is what freezes
		// the archive (NEW-6). Only a target with NO existing settled version
		// (a fresh promote/first setup) discovers the newest point release.
		version, have := existingDVDVersion(store, t)
		if !have {
			v, verr := debianDVDVersion(ctx, o, params) // newest point release for the suite
			if verr != nil {
				slog.Warn("cache: debian dvd version resolve failed; retry next tick", "target", t.ID, "err", verr)
				return nil
			}
			version = v
		}
		if err := ensureDebianDVD(ctx, store, t, version); err != nil {
			slog.Warn("cache: debian dvd ensure failed; retry next tick", "target", t.ID, "err", err)
		}
		return nil
	}

	existing, err := store.ListTargetVersions(t.ID)
	if err != nil {
		return fmt.Errorf("cache: list versions for target %d: %w", t.ID, err)
	}
	var manual []string
	for _, v := range existing {
		if v.Source == "manual" {
			manual = append(manual, v.Version)
		}
	}

	// Resolve the retained discovered set (empty for manual-only targets, and
	// left empty on a discovery-fetch failure so nothing is pruned).
	var retained []string
	pruneDiscovered := false
	if t.Mode == "discovery" {
		discovered, derr := o.DiscoverVersions(ctx, params)
		if derr != nil {
			slog.Warn("cache: discovery failed; keeping existing cached set", "os", t.OS, "target", t.ID, "err", derr)
		} else {
			// #48 §8: the retention window ranges over discovered ∪ (in-window
			// AND cached AND discovered), so single-version-discovery OSes
			// (flatcar/fcos) accumulate history release-by-release under
			// retainN>1. The in-window+cached+discovered source keeps archived
			// versions from resurrecting, is the guard P3b's bytes-less failure
			// rows rely on, and excludes manual pins — always desired, never
			// archived, so counting them would only displace a discovered
			// version instead of adding coverage. Evicted versions cannot
			// return: eviction deletes the target_versions row entirely.
			inWindow, werr := store.ListCachedInWindowVersions(t.ID)
			if werr != nil {
				return fmt.Errorf("cache: list in-window %d: %w", t.ID, werr)
			}
			known := slices.Clone(discovered)
			for _, v := range inWindow {
				if !slices.Contains(known, v) {
					known = append(known, v)
				}
			}
			retained = retentionFor(t.OS, known, t.RetainN)
			pruneDiscovered = true
		}
	}

	cacheName := canonicalToCacheName(t.OS)
	segment := paramSegment(params)

	// Prior-tick cached state, read BEFORE the desired loop's per-version upserts
	// reset cached=0. The land-path idempotency skip below consults it.
	cachedByVersion := make(map[string]bool, len(existing))
	for _, v := range existing {
		cachedByVersion[v.Version] = v.Cached
	}

	// Upsert + ensure-artifacts for every desired version (retained discovered +
	// all manual pins). Manual rows keep source="manual".
	desired := append(slices.Clone(retained), manual...)
	for _, version := range desired {
		source := "discovered"
		if slices.Contains(manual, version) {
			source = "manual"
		}
		// D6: a version rejected by verification is not retried until
		// verifyRetryAfter elapses. Placed BEFORE o.Artifacts so a guarded
		// version also skips the upstream metadata fetch (for Tails, the
		// per-release sidecar GET) — both placements converge, only this one
		// avoids the fetch.
		//
		// Version-level and OS-agnostic on purpose: every OS reaching this loop
		// gets the same rate limit. Debian DVD targets return above this loop and
		// so never reach this guard, but they are bounded by the SAME window
		// through a separate mechanism — ensureDebianDVD reads a marker file with
		// dvdVerifyGuarded (pkg/cache/debiandvd.go). Two readers, one window; see
		// verifyRetryAfter above.
		//
		// A transport failure ALONE is never guarded: with no artifact refused,
		// the version defers before any cache_entries row is written, so it
		// cannot forge the four-column signature VerifyRejectedWithin matches
		// on. A transport failure co-occurring with a REFUSAL is a different
		// case: the refusal is recorded and does arm the guard, which is
		// deliberate — see artifactOutcome.rejected below.
		blocked, guardReason, gerr := store.VerifyRejectedWithin(t.ID, version, verifyRetryAfter)
		if gerr != nil {
			// No ids: db.VerifyRejectedWithin already wraps as
			// "db: verify-rejected guard %d/%s", so repeating them double-prints.
			return fmt.Errorf("cache: verify-retry guard: %w", gerr)
		}
		if blocked {
			// A RELATIVE bound, not an absolute next-attempt time: computing the
			// latter needs either a Go-side parse of the UTC fetched_at TEXT
			// column (which yields a future timestamp — see
			// db.VerifyRejectedWithin) or a second SQL expression. The guard
			// suppresses only the RE-DOWNLOAD; the verdict and verify_err stay
			// recorded and API-exposed.
			slog.Warn("cache: version rejected by verification; not retrying yet",
				"os", t.OS, "arch", t.Arch, "version", version,
				"verifyErr", guardReason, "retryAfter", verifyRetryAfter)
			continue
		}

		dir := cacheDir(cacheName, segment, t.Arch, version)
		arts, aerr := o.Artifacts(ctx, version, t.Arch, params)
		if aerr != nil {
			// No artifact list → cannot evaluate the skip guard, so do NOT touch
			// the row: a settled version keeps its cached=1 through a transient
			// upstream blip (the #48-window drop this fix also guards against).
			slog.Warn("cache: artifacts unavailable; skipping version this tick", "os", t.OS, "version", version, "err", aerr)
			continue
		}

		// Idempotency (restores the retired ensureArtifact's skip-if-present): a
		// version already cached=1 in the prior snapshot whose every final file is
		// on disk is SETTLED — skip the whole version (no re-download, no
		// re-verify, and critically no cached=0 reset), instead of re-running a
		// full HTTP GET for every artifact each CacheInterval tick.
		//
		// Safety: cached=1 is set ONLY on the successful all-landed path (never on
		// a rejected/failed version), so the skip cannot admit a version that
		// never passed admission; finalFilesPresent forces the full land path when
		// any byte is missing. The verified column is intentionally NOT consulted:
		// a legitimately not-verifiable version (Talos/Debian have no sha/sig, or a
		// version landed under `off`) records verified=NULL yet is fully settled,
		// and gating the skip on non-NULL verified would re-download it forever
		// under the default `warn`. Policy tightening is NOT retroactive: a version
		// admitted under a looser prior policy stays on disk until the reverify
		// endpoint re-checks it on demand — matching the design (§5, D15); no
		// retroactive re-verification happens here.
		if cachedByVersion[version] && finalFilesPresent(dir, arts) {
			continue
		}

		if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: t.ID, Version: version, Source: source}); err != nil {
			return fmt.Errorf("cache: upsert %d/%s: %w", t.ID, version, err)
		}
		policy := viper.GetString(config.SignaturePolicy)
		outcomes := make([]artifactOutcome, len(arts))
		vg := new(errgroup.Group)
		vg.SetLimit(max(concurrency, 1))
		for i, a := range arts {
			vg.Go(func() error {
				landed, v, err := landArtifact(ctx, dir, a, policy)
				if err != nil {
					slog.Warn("cache: artifact fetch failed", "os", t.OS, "version", version, "file", a.Filename, "err", err)
				}
				outcomes[i] = artifactOutcome{landed: landed, verdict: v, err: err}
				return nil
			})
		}
		// Every goroutine returns nil: a transport error is carried per-artifact
		// in outcomes[i].err so it can be ATTRIBUTED, which the group's single
		// first-error cannot do. The group is a bounded waiter here, nothing more
		// — so Wait's value is structurally always nil. REINTRODUCING `return err`
		// above would make this line SWALLOW that error rather than route it;
		// carry it in the outcome instead.
		_ = vg.Wait()

		if ctx.Err() != nil {
			// Shutting down: loop() returns on ctx.Done() but does not preempt an
			// in-flight pass, so this one keeps running with a dead context. A
			// cancelled sidecar fetch is classified as corruption with a NIL error
			// (verify.go's verifyDetachedGPG), which reads here as a REFUSAL — so no
			// refusal from this pass is trustworthy. DEFER, exactly as before this
			// disposition existed: nothing recorded, nothing wiped, resumable bytes
			// kept.
			continue
		}

		rejected := slices.ContainsFunc(outcomes, artifactOutcome.rejected)
		errored := 0
		for _, o := range outcomes {
			if o.err != nil {
				errored++
			}
		}

		// A refusal WINS over a co-occurring transport error: it is knowledge a
		// retry will not change, and recording it is what arms the retry guard
		// below. A transport error with NO refusal still writes no cache_entries
		// row — which is what keeps it from forging VerifyRejectedWithin's
		// four-column signature, and what leaves a resumable <file>DownloadSuffix
		// file on disk to resume next tick (D4b). (The loop-entry
		// UpsertTargetVersion above has already written cached=0; the guard reads
		// cache_entries, not that column.)
		//
		// The accepted cost: a GENUINELY transient co-occurrence — upstream
		// mid-publish, say, with a new sidecar against old bytes and a sibling not
		// yet uploaded — now holds a NEW version back for up to verifyRetryAfter
		// where it would previously retry within minutes. Versions live for weeks
		// and the guard clears itself, so this trades minutes of freshness for a
		// bounded re-download loop.
		if !rejected && errored > 0 {
			continue
		}

		tvID, verr := store.TargetVersionID(t.ID, version)
		if verr != nil {
			return fmt.Errorf("cache: resolve tv id %d/%s: %w", t.ID, version, verr)
		}
		verdicts := make([]artifactVerdict, len(outcomes))
		for i, o := range outcomes {
			verdicts[i] = o.verdict
		}
		verified, verifyErr := aggregateVerdicts(verdicts)

		if rejected {
			// Version REJECTED (a failure the policy refuses to land). Version-level
			// atomicity: wipe the partial-or-landed dir so NewestCached falls back
			// to the prior cached version (§6), and record a failure-visibility row.
			if err := removeVersionDir(cacheName, segment, t.Arch, version); err != nil {
				slog.Warn("cache: remove rejected version dir failed", "os", t.OS, "version", version, "err", err)
			}
			// D-D: verifyErr is non-empty for every reachable refusal —
			// aggregateVerdicts always attaches a message to
			// classCorruption/classForgery. An empty one would write this row and
			// leave the retry guard DISARMED, because its predicate requires a
			// non-empty verify_err. That is left undefended on purpose: the path
			// is doubly unreachable, and an unarmed guard is the safe direction.
			if err := store.UpsertCacheEntryArchived(tvID, verifyErr); err != nil {
				return fmt.Errorf("cache: record rejected %d/%s: %w", t.ID, version, err)
			}
			// erroredSiblings is the ONLY place a co-occurring transport error
			// surfaces: verify_err stays verification-only by design, so the
			// operator learns from this line that part of the version could not
			// be evaluated at all.
			slog.Error("cache: version rejected by verification",
				"os", t.OS, "version", version, "policy", policy, "err", verifyErr,
				"erroredSiblings", errored)
			continue
		}

		// All artifacts landed → mark cached, record size + verdict.
		if err := store.UpsertTargetVersion(db.TargetVersion{TargetID: t.ID, Version: version, Source: source, Cached: true}); err != nil {
			return fmt.Errorf("cache: mark cached %d/%s: %w", t.ID, version, err)
		}
		var size int64
		for _, a := range arts {
			p, perr := artifactPath(dir, a.URL)
			if perr != nil {
				continue
			}
			if fi, serr := os.Stat(p); serr == nil {
				size += fi.Size()
			}
		}
		if err := store.UpsertCacheEntry(tvID, size); err != nil {
			return fmt.Errorf("cache: upsert cache_entry %d/%s: %w", t.ID, version, err)
		}
		if verified != nil { // NULL (off / not-verifiable) leaves the P3a column untouched
			if err := store.SetCacheVerified(tvID, verified, verifyErr); err != nil {
				return fmt.Errorf("cache: record verdict %d/%s: %w", t.ID, version, err)
			}
			if !*verified {
				// Landed WITH a verification failure — reachable only under `warn`
				// (strict rejects the version above; off records verified=NULL, so
				// verified==nil here). The point of `warn` is to warn the operator;
				// the silent verified=0 DB flag is not that, so emit the WARN the
				// design + CONFIGURATION.md promise. Observability only — the
				// land/reject decision was already made by landArtifact.
				slog.Warn("cache: landed artifact with failed verification (signaturePolicy=warn)",
					"os", t.OS, "arch", t.Arch, "version", version, "verifyErr", verifyErr)
			}
		}
	}

	// P3a: rotated-out DISCOVERED versions are ARCHIVED (in_window=0), not deleted
	// — disk is kept so they stay menu-bootable (rollback); size-based eviction
	// (evict.go) reclaims oldest archived-unpinned over cacheMaxBytes. Manual rows
	// are never touched. Mark rotated-out discovered rows archived;
	// SetCacheInWindow is a no-op when no cache_entries row exists yet.
	if pruneDiscovered {
		for _, v := range existing {
			if v.Source != "discovered" || slices.Contains(retained, v.Version) {
				continue
			}
			if err := store.SetCacheInWindow(v.ID, false); err != nil {
				return fmt.Errorf("cache: archive %d/%s: %w", t.ID, v.Version, err)
			}
		}
	}
	return nil
}
