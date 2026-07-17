# Schema & contracts

This directory documents booty's structural contracts — the interfaces other systems and operators
depend on. It reflects the **current** implementation; each is updated as the relevant slice of the
v1 management plane lands.

- **[API.md](API.md)** — the HTTP endpoints, the TFTP boot filenames and their token substitution,
  and proxyDHCP behavior.
- **[DATABASE.md](DATABASE.md)** — the host database (`hardware.json`) record shape and the on-disk
  version-metadata files. (The v1 plane migrates this state into SQLite.)
- **[STORAGE.md](STORAGE.md)** — the `--dataDir` layout and the artifact-cache directory structure.
- **[CATALOG.md](CATALOG.md)** — the declarative `catalog.yaml` schema, precedence, and reconcile
  semantics for cache targets.

Together these answer: *what can I call, what gets stored, and where does it live on disk.*
