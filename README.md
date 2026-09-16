# signum

Track the physical prototypes you print. Upload an STL — or a folder or zip
of them — describe it (version, variant, CAD links, your own fields), and it
engraves a short uid into a face you pick and keeps everything. Find a part
in a drawer a year later, type the uid, and see exactly what it is. Point it
at your printers and every job is recorded against the part it printed, with
the printer, filament, and settings that were actually used.

One Go binary:

- `signum serve` — the hosted server, used through the browser.
- `signum desktop` — the same thing on your own machine: local database,
  works fully offline and signed out, can mirror projects to folders on disk.
- `signum watch` — a headless agent for a Raspberry Pi on the same network
  as the printers, reporting jobs to a server.

Sign-in, when you want it, is "sign in with GitHub or Discord" through the
[identity service](https://github.com/basicallysource/identity); this service
never sees a password. To host it that way:

    signum serve --identity https://identity.example --base https://signum.example

with `SIGNUM_SESSION_KEY` in the server's environment: 32 random bytes as
hex, made once with `openssl rand -hex 32`. It seals the session cookie, so
keep it secret and keep it: changing it signs everybody out. The identity
service must allow `https://signum.example/auth/callback` as a redirect. A
watcher reports with an identity account token (`signum watch --token`).

Early. The engraver and the upload flow land first; printer drivers are
growing one at a time. See `agent-docs/architecture.md` for how it is put
together and `agent-docs/design.md` for how it looks.
