# ExpSync

A Fyne desktop app for syncing experiment data between local folders and cloud
remotes (SharePoint/OneDrive, Google Drive, Dropbox, S3) via rclone.

## Layout

- `main.go` — entry point.
- `internal/ui` — Fyne screens (one file per screen) and shared widgets.
- `internal/syncengine` — rclone-backed sync/copy/preview logic and Location model.
- `internal/rcbackends` — remote backend definitions and field metadata.
- `internal/appconfig` — persisted config (Locations, filters, preferences).

## Data schema

This app is built around the lab's data storage schema (experiment directories,
recorder directories, metadata.csv/README.txt, audio-file naming, and the three
synced locations). See [SCHEMA.md](SCHEMA.md) and reference it for any work that
touches how experiment data is structured, previewed, or validated (e.g. an
option to preview experiments).

## Imperatives

- Always use the OS-native file browser, never Fyne's in-app file/folder
  browser. Route all folder and file picking through `chooseFolder`,
  `chooseFileSave`, and `chooseFileOpen` (see `folder_picker_darwin.go` for the
  native implementation and `folder_picker_other.go` for the fallback).
- Never duplicate logic. If an existing pathway needs to be used in another
  place, extract it into a shared abstraction and call it from both, rather
  than copying it.
- Window-stretches-across-monitors bug: Fyne sets a window's min size from its
  content's min size, so a wide child forces the window wider than windowSize.
  This is fixed universally in `state.setContent`, which wraps content in
  `boundedWidthLayout` (caps min width to windowSize). So: always swap content
  via `setContent`, never `win.SetContent`. Set `Truncation` on labels holding
  long paths for looks, but the window itself can no longer be stretched.
- Do not commit changes until the user has tested and verified them working.
- Worktrees go in ./.claude/worktrees
- **rclone must always use `copy`, never `sync`** — this is a core safety
  invariant. `rclone sync` deletes destination-only files; this app is not
  authorized to delete data ever. The UI intentionally uses the word "sync"
  for end-user clarity (researchers understand "sync" intuitively), but the
  underlying rclone command is always `copy`. Never change this without an
  explicit, informed decision by the project owner.