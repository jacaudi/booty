CREATE TABLE discovered_hosts (
    mac        TEXT PRIMARY KEY,
    arch       TEXT NOT NULL DEFAULT '' CHECK (arch IN ('', 'amd64', 'arm64')),
    ip         TEXT NOT NULL DEFAULT '',
    first_seen TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- create-host now stores the boot vocabulary (design D9), so rewrite rows stored before it
UPDATE hosts SET os = 'coreos' WHERE os = 'fedora-coreos';
UPDATE hosts SET assigned_os = 'coreos' WHERE assigned_os = 'fedora-coreos';
