# Architecture

The committed decisions and their reasons. Change a decision, change this
file in the same commit.

## What it is

A tracker for 3D-printed prototype parts. You hand it an STL (or a folder or
zip of them) and describe it — version, variant, CAD links, whatever fields
you care about — and it engraves a short uid into the part and keeps the
files. Type a uid off a physical part and it tells you exactly what that
part is. Connect it to your printers and every print job is recorded against
the part it printed, with the parameters that were used: which printer, what
filament, what settings, success or failure.

## The stack: one Go binary, no Node

Decided 2026-08-27, after starting from an Electron + SvelteKit sketch and
throwing it out:

- **Everything is Go.** The engraving is geometry, the server is HTTP, the
  printer agent must run on a Raspberry Pi, and releases must exist for
  macOS, Linux, and Windows. One language cross-compiles to all of that as
  static binaries measured in megabytes; Electron starts at ~100MB per
  platform and drags a second toolchain (Node) into a project that
  otherwise has none.
- **The UI is server-rendered HTML.** Templates plus the design tokens in
  `agent-docs/design.md`, with small amounts of vanilla JS where the page is
  genuinely interactive (upload, the face picker). No frontend build step at
  all. Any JS library we adopt (e.g. htmx, a 3D viewer) is vendored as one
  pinned file, never a package manager.
- **The desktop app is the same binary.** `tracker desktop` runs the same
  server against a local database and opens the browser. If a native window
  ever matters, a webview shell wraps this same HTTP surface — that is a
  packaging decision for later, deliberately not a second frontend. It must
  work fully signed out (local-only); signing in syncs to a server.
- **One repo, one module.** The server, the desktop mode, the printer agent,
  and the engraver are packages of one module, released together.

## Committed decisions

- **Identity is not this service's problem.** Sign-in is the identity
  service: this server accepts its opaque bearer tokens and asks its
  `/v1/whoami` who a token belongs to (cached briefly). Accounts here are
  identity account ids. Local/desktop mode has no accounts at all.
- **The engraver is pure Go and boolean-free** (`engrave/`). It re-triangulates
  the one planar facet a pocket intrudes into rather than running a general
  mesh boolean, because no trustworthy manifold/CSG library exists in pure Go
  and a subprocess sidecar (Python + trimesh) would end the single-binary
  story. Flat faces only for now; curved walls are future work. The font is
  fetched by pinned URL + SHA-256 at first use, never committed.
- **The uid is 6 characters of lowercase base36**, random, never all digits,
  unique per server. It names a described upload — the design revision you
  stamped — not the bytes of one file. `/u/<uid>` resolves it forever.
- **Engraving is a choice per upload, not a property of the part.** The
  upload form offers checkboxes: uid (default on), filename, a free version
  line. The chooser cycles through ranked candidate faces; both the original
  and engraved STLs are kept.
- **Fields are free-form and remembered per person.** Beyond the built-ins
  (version, variant, CAD links, notes), people add their own named fields;
  names you have used before are suggested next time, from history rather
  than a preferences table.
- **Projects nest.** A project is a folder that can hold parts and other
  projects. The desktop app can mirror a project to a chosen local folder so
  engraved files land where you work.
- **Printer watching is one package with two thin faces**
  (`internal/printwatch`). The desktop app and the headless Pi agent
  (`tracker watch`) are the same watcher: drivers poll printers on the local
  network, emit job events (file, timestamps, outcome, parameters), and a
  sink either writes the local database (desktop, signed out) or posts to a
  server with a token (the Pi, or a signed-in desktop). This is not
  over-abstraction; it is the only way the two stay the same thing.
- **Matching a print to a part** is by exact file hash when the agent can get
  the file, else by filename. Engraved filenames carry the uid
  (`name-<uid>.stl`), which makes filename matching nearly exact in practice.
- **Storage is SQLite plus a content-addressed blob directory.** Uploaded
  bytes are stored once under their SHA-256; the database stores references.
  A hosted deployment can later move blobs to object storage behind the same
  interface without touching handlers.

## Layout

    cmd/tracker           the binary: serve / desktop / watch subcommands
    engrave/              STL in, ranked faces, text pocket out (pure Go)
    internal/store        SQLite: projects, parts, files, fields, printers, jobs
    internal/web          handlers + templates + the stylesheet
    internal/printwatch   printer drivers and the event pipeline
    internal/blob         content-addressed file storage

## Not built yet, and where it goes

- Curved-face engraving (cylinder unroll) — `engrave/`, behind the same
  Placement API.
- Real printer drivers. The driver interface and a mock exist; Moonraker
  (Klipper), PrusaLink, OctoPrint, and Bambu each become one file in
  `internal/printwatch/driver/`.
- Server-side blob storage on the asset service — a second implementation of
  `internal/blob`, chosen by configuration.
- Desktop webview shell and OS packaging (signed .app, .msi) — wraps
  `tracker desktop`, changes nothing above it.
