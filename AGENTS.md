# AGENTS.md

Assume everything committed and tracked in this repository is public, and act
accordingly. Nothing environment- or deployment-specific goes in tracked
files: no hostnames, no machine names, no secrets, no credential names, no
operational details, no personal file paths.

No binary files in git: no STLs, no fonts, no images. Test fixtures are
generated in code; larger assets are fetched by pinned URL + SHA-256.

Read `agent-docs/architecture.md` before changing structure and
`agent-docs/design.md` before touching UI. When a decision changes, update the
doc in the same change. Delete obsolete code in the same change that replaces
it; git is the backup.

If a gitignored `AGENTS.local.md` exists next to this file, read it too: it
carries the private working context that never gets committed.
