package cache

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jeefy/booty/pkg/config"
	"github.com/jeefy/booty/pkg/db"
	"github.com/spf13/viper"
)

// Reconciler eagerly caches each target's artifacts. A single coordinator
// goroutine owns every DB upsert/prune (no viper/db races); artifact downloads
// are bounded inside reconcileTarget by a fresh per-version errgroup with
// SetLimit(concurrency). Triggers in P1b: startup + a periodic tick. The trigger
// is an internal seam (fire()); P1c will add an exported Trigger() wrapper
// (additive) for the API-mutation producer — not built now (YAGNI).
type Reconciler struct {
	store       *db.Store
	interval    time.Duration
	concurrency int
	catalog     []CatalogEntry

	trigger  chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewReconciler builds a reconciler over store. interval is the periodic tick
// (config.CacheInterval); concurrency is the per-version artifact-download cap
// (config.CacheConcurrency) applied inside reconcileTarget; catalog is the
// loaded declarative catalog (cache.LoadCatalog) applied every tick.
func NewReconciler(store *db.Store, interval time.Duration, concurrency int, catalog []CatalogEntry) *Reconciler {
	return &Reconciler{
		store:       store,
		interval:    interval,
		concurrency: max(concurrency, 1),
		catalog:     catalog,
		trigger:     make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
}

// Start launches the coordinator goroutine, fires an immediate startup
// reconcile, and runs the periodic ticker until Stop or ctx cancellation.
func (r *Reconciler) Start(ctx context.Context) {
	r.wg.Go(func() { r.loop(ctx) }) // Go 1.25 wg.Go: no manual Add/Done
	r.fire()                        // startup reconcile
}

// Stop signals the coordinator to exit and waits for it to drain.
func (r *Reconciler) Stop() {
	r.stopOnce.Do(func() { close(r.done) })
	r.wg.Wait()
}

// fire requests a reconcile without blocking (coalesced: a pending request is
// enough). P1c's API-mutation producer will call this same path.
func (r *Reconciler) fire() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// Trigger requests an asynchronous reconcile from outside the package — the
// API-mutation producer (P1c) calls this after a target/version change. It is a
// thin exported wrapper over the internal coalescing fire(); a pending request
// is enough, so bursts of mutations collapse to one reconcile.
func (r *Reconciler) Trigger() { r.fire() }

func (r *Reconciler) loop(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.done:
			return
		case <-r.trigger:
			r.reconcileAll(ctx)
		case <-ticker.C:
			r.reconcileAll(ctx)
		}
	}
}

// reconcileAll applies the catalog and host schematics, then reconciles every
// enabled target sequentially. Sequential target processing keeps all DB
// writes on this goroutine; download concurrency is bounded inside
// reconcileTarget (a fresh per-version errgroup capped at r.concurrency). A
// per-target error is logged and the loop continues (one bad target never
// blocks others).
func (r *Reconciler) reconcileAll(ctx context.Context) {
	if err := SweepPartials(cacheRoot()); err != nil {
		slog.Warn("cache: sweep partials failed", "err", err)
	}
	if err := applyCatalog(r.store, r.catalog); err != nil {
		slog.Warn("cache: apply catalog failed", "err", err)
	}
	if err := reconcileHostSchematics(r.store); err != nil {
		slog.Warn("cache: reconcile host schematics failed", "err", err)
	}
	targets, err := r.store.ListTargets()
	if err != nil {
		slog.Warn("cache: list targets failed", "err", err)
		return
	}
	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		if err := reconcileTarget(ctx, r.store, r.concurrency, t); err != nil {
			slog.Warn("cache: reconcile target failed", "os", t.OS, "arch", t.Arch, "target", t.ID, "err", err)
		}
	}
	if err := evictOverBudget(r.store, viper.GetInt64(config.CacheMaxBytes)); err != nil {
		slog.Error("cache: eviction pass failed", "err", err)
	}
}
