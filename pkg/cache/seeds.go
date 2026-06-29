package cache

import "github.com/jeefy/booty/pkg/db"

// seedTargets upserts the predefined OS targets into the store. This stub is
// replaced by T6 (predefined targets) with the real implementation; in T5 the
// reconciler test seeds its own targets directly via db.Store.CreateTarget.
func seedTargets(_ *db.Store) error { return nil }
