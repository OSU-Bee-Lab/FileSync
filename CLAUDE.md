# FileSync

A Fyne desktop app for syncing experiment data between local folders and cloud
remotes (SharePoint/OneDrive, Google Drive, Dropbox, S3) via rclone.

## Layout

- `main.go` — entry point.
- `internal/ui` — Fyne screens (one file per screen) and shared widgets.
- `internal/syncengine` — rclone-backed sync/copy/scan logic and Location model.
- `internal/rcbackends` — remote backend definitions and field metadata.
- `internal/appconfig` — persisted config (Locations, filters, preferences).
- `internal/audio` — streamed playback of recordings for in-app preview, with
  one driver per format under `internal/audio/drivers`.
- `internal/recorder` — recorder detection, byte-for-byte offload/verify, and
  clock-timestamp handling, with one driver per recorder model under
  `internal/recorder/drivers`.
- `internal/appversion` — app version string.

## Data schema

This app is built around the lab's data storage schema (experiment directories,
recorder directories, metadata.csv/README.txt, audio-file naming, and the three
synced locations). See [SCHEMA.md](SCHEMA.md) and reference it for any work that
touches how experiment data is structured, scanned, or validated (e.g. an
option to scan experiments).

## Imperatives

- Always use the OS-native file browser, never Fyne's in-app file/folder
  browser. Route all folder and file picking through `chooseFolder`,
  `chooseFileSave`, and `chooseFileOpen` — each platform has its own native
  implementation (`folder_picker_darwin.go`, `folder_picker_linux.go`,
  `folder_picker_windows.go`), with `folder_picker_fyne_fallback.go` /
  `folder_picker_other.go` as the fallback for anything else.
- Never duplicate logic. If an existing pathway needs to be used in another
  place, extract it into a shared abstraction and call it from both, rather
  than copying it.
- Window-stretches-across-monitors bug: Fyne sets a window's min size from its
  content's min size, so a wide child forces the window wider than windowSize.
  This is fixed universally in `state.setContent`, which wraps content in
  `boundedWidthLayout` (caps min width to windowSize). So: always swap content
  via `setContent`, never `win.SetContent`. Set `Truncation` on labels holding
  long paths for looks, but the window itself can no longer be stretched.
- Commits may be made freely and at-will on the `development` branch — no need
  to wait for the user to test and verify changes first. Never commit or push
  to `main` without explicit user confirmation first.
- This is a cross-platform desktop GUI app (Fyne; macOS, Linux, Windows) with
  no screenshot/automation harness. Never attempt to "visually verify" UI
  changes yourself (launching the app to screenshot it, click through it,
  etc.) — you have no way to see a native window. Build/vet/test to confirm
  it compiles and passes existing tests, then hand off to the user to check
  it in the running app.
- Worktrees go in ./.claude/worktrees
- Any user-facing count text (labels, buttons, dialogs, tooltips, status
  text) must be plural-aware. Never hedge with "(s)"/"(y/ies)" (e.g. "3
  conflict(s)"), and never hardcode one form next to a bare `%d` (e.g. "All
  %d recorder start times line up" reads wrong at n=1). Use `plural(n,
  singular, pluralForm)` / `pluralWord(n, singular, pluralForm)` from
  `internal/ui/util.go` for every new `%d`-driven string, and hand-pick verb
  agreement from the same count where the sentence has one ("1 recorder
  looks off" vs "2 recorders look off").
- Folder/experiment browsers' "step up one directory" action must use
  `widget.NewButtonWithIcon("", theme.NavigateBackIcon(), ...)`, never a text
  button (e.g. "Up"). See `dest_folder_browser.go`'s `backBtn` for the
  reference implementation.
- **rclone must always use `copy`, never `sync`** — this is a core safety
  invariant. `rclone sync` deletes destination-only files; this app must
  never delete data from a synced destination — with the single narrow,
  user-gated exception of N-way conflict resolution (see below). The UI
  intentionally uses the word "sync" for end-user clarity (researchers
  understand "sync" intuitively), but the underlying rclone command is
  always `copy`. Never change this without an explicit, informed decision
  by the project owner.
- This never-delete rule scopes to rclone/cloud destinations only. It does
  not cover `internal/recorder`'s recorder-side deletion: once a file has
  been copied off a recorder (Sony ICD-PX370, Olympus VN-541PC, ...) and
  verified byte-for-byte, deleting it from the recorder's own storage is
  intentional and user-toggleable (`RecorderSettings.AutoDeleteAfterVerify`)
  — it's how a recorder gets reset for reuse in the field, not data loss.
- The one authorized deletion from a synced destination is N-way conflict
  resolution (`syncengine.NWayDelete` / `DeleteConflictFile`): when two or
  more locations hold genuinely different content at the same path, the user
  may choose to delete a specific location's copy. It is gated — they see
  every divergent copy with its size and location, pick deletion deliberately
  per file, and confirm an irreversible-action prompt. It must never be
  reachable as a default, automatically, or without that confirmation. All
  other N-way propagation stays copy-only: it never deletes a file from a
  location just because another location lacks it.
- The never-delete rule above scopes to the automated sync/copy pathway
  between Locations (`rclone copy`, N-way propagation) — it is not a
  blanket ban on deletion anywhere in the app. **Manage Files**
  (`internal/ui/screen_manage_files.go`, `internal/syncengine/manage.go`)
  is a second, deliberate, narrowly-scoped exception, by explicit,
  informed decision of the project owner: a tool where the user directly
  renames, moves/merges, or deletes files/dirs within experiment data,
  including permanent deletion. It is user-driven, not automatic —
  reachable only from the main menu, only after the user browses to or
  types the exact path, previews the final state, resolves any collisions,
  and (for delete) types the exact relative path plus confirms an
  irreversible-action prompt. Like N-way conflict deletion, it must never
  trigger automatically or as a side effect of a scan/sync operation.