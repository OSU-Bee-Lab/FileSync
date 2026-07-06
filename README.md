# ExpSync

A small cross-platform (Linux/macOS/Windows) desktop app for the OSU Bee
Lab's bioacoustics data schema. It wraps [rclone](https://rclone.org)
(embedded as a Go library, not a bundled/shelled-out binary) so that
colleagues who aren't comfortable with the CLI can:

- back up one experiment at a time between two storage locations (never the
  whole multi-terabyte `experiments/` tree at once - that's what made the
  old hand-typed `rclone copy` commands slow), or
- download a sub-path of one experiment (a single deployment date, a single
  recorder) into a working folder, e.g. for analysis in R,

with a mandatory scan before anything is copied, and never a
`sync`/delete operation - only additive `copy`.

Nothing about a storage location (path, remote type, `experiments/` folder
name, file filter) is hardcoded, so this works for other labs' schemas and
storage setups too, as long as they follow the same
`[location]/experiments/[experiment name]/...` shape.

## Using the app

1. **Manage Locations** - add each storage root you use (a local
   folder/drive, or a remote like SharePoint/OneDrive, Google Drive,
   Dropbox, or S3). For a remote, the wizard drives rclone's own OAuth
   sign-in flow and opens your browser automatically.
2. **Backup / Sync** - pick a source and destination Location, select one
   or more experiments, scan, confirm, copy.
3. **Download** - pick a source Location, drill into its `experiments/`
   tree to whatever depth you want (a whole experiment, one date, one
   recorder), choose any local folder as the destination, scan, confirm.
   Files land at `<destination>/<the folder you picked>/...`, preserving
   that structure rather than dumping everything flat into the destination
   root.

Locations and their filter/mtime defaults are saved per-machine
(`~/Library/Application Support/ExpSync/config.json` on macOS,
`~/.config/ExpSync/config.json` on Linux, `%AppData%\ExpSync\config.json` on
Windows). Remote credentials are saved in rclone's own default config file,
untouched - if you also have the real `rclone` CLI installed, it will see
the same remotes ExpSync creates.

## First run: "unknown developer" warnings

There's no code-signing budget behind this app, so the OS will flag the
first launch. This doesn't mean anything is wrong - just click through it
once:

**macOS (Sequoia and later):** double-clicking will be blocked outright.
Go to **System Settings → Privacy & Security**, scroll to the bottom, and
click **"Open Anyway"** next to the ExpSync mention, then confirm once
more. (Older macOS versions may instead allow right-click → Open as a
shortcut - if you see that option, it works too.)

**Windows:** SmartScreen will show "Windows protected your PC". Click
**"More info"**, then **"Run anyway"**.

Whichever storage channel you got the app through, this only needs doing
once per machine per build - most friction disappears entirely if the app
is shared over a local network drive/USB rather than downloaded through a
browser, since the OS only flags files that came from an actual internet
download.

## Building

Requires Go 1.25+ and, for packaging, `fyne-cross`:

```sh
go install github.com/fyne-io/fyne-cross@latest
```

`fyne-cross` needs a running Docker (or Podman) connection for *every*
target, including macOS, despite its `-local` flag's documented default -
this was verified against v1.6.2, where the container-engine setup step
still requires a live Docker/Podman API even for local-arch builds. Start
Docker Desktop (or `colima start` on macOS) before building.

```sh
# quick local dev run, no packaging (needs a live display; skips fyne-cross/Docker)
go run .

# packaged builds (each needs Docker/Podman running - see above)
make -f build/Makefile build-darwin
make -f build/Makefile build-windows
make -f build/Makefile build-linux

# all three, collected into release/<VERSION>/
make -f build/Makefile release VERSION=v0.1.0
```

`Icon.png` at the repo root is a placeholder - swap it for real branding
before a real release.

## Testing

```sh
go test ./...
```

`internal/syncengine`'s tests run entirely against local temp directories
(no real remote/network access needed) and include a named regression test,
`TestCopyPreserving_NeverDeletesDestinationOnlyFiles`, guarding the one
behavior that would be catastrophic if a future change accidentally used
rclone's `sync` semantics instead of `copy`.

## Safety rules baked into the design

- Every copy operation uses rclone's `sync.CopyDir`, never `sync.Sync` -
  destination-only files are never deleted, matching the lab's existing
  "never use `rclone sync`" rule.
- Every real copy is preceded by a scan the user must explicitly
  confirm.
- The app expects the lab's schema (experiment directories directly under
  `experiments/`, each containing `metadata.csv`/`README.txt`) for browsing
  purposes, but never enforces or validates it - nothing is blocked on a
  missing file or an unexpected name.
