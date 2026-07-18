-- 0008: Debian DVD support — mutable serving-mode columns on targets.
-- NOT part of identity UNIQUE(os,arch,params). Plain ADD COLUMN (no rebuild):
-- SQLite permits ADD COLUMN with a constant DEFAULT and a CHECK constraint.
ALTER TABLE targets ADD COLUMN source_mode TEXT NOT NULL DEFAULT 'netinst'
    CHECK (source_mode IN ('netinst','dvd'));
ALTER TABLE targets ADD COLUMN dvd_count INTEGER NOT NULL DEFAULT 1;
ALTER TABLE targets ADD COLUMN desired_mode TEXT
    CHECK (desired_mode IN ('netinst','dvd'));
