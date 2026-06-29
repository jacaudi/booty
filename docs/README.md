# booty documentation

This directory holds booty's tracked, self-documenting reference material. It describes **what the
code does today**; new capabilities are documented here as they land.

> Implementation **plans** and **design** documents are intentionally *not* tracked — they live
> under `docs/plans/` (git-ignored, local-only). What you find here is the committed,
> ships-with-the-code documentation.

## Contents

- **[CONFIGURATION.md](CONFIGURATION.md)** — the complete flag and environment-variable reference,
  plus the network ports booty listens on.
- **[schema/](schema/)** — the structural contracts:
  - **[schema/API.md](schema/API.md)** — HTTP endpoints, TFTP boot filenames + token substitution,
    and proxyDHCP behavior.
  - **[schema/DATABASE.md](schema/DATABASE.md)** — the host database (`hardware.json`) and the
    on-disk version-metadata records.
  - **[schema/STORAGE.md](schema/STORAGE.md)** — the data-directory layout and the artifact cache
    structure.

## See also

- The root **[README](../README.md)** — what booty is, what it does, and a quickstart.
- **[examples/](../examples/)** — sample config templates and helper scripts.
