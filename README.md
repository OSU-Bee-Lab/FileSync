# FileSync

<img src="Icon.png" alt="FileSync logo" width="120"/>

[![Latest release](https://img.shields.io/github/v/release/OSU-Bee-Lab/FileSync)](https://github.com/OSU-Bee-Lab/FileSync/releases/latest)

A small cross-platform (Linux/macOS/Windows) desktop app for the OSU Bee Lab's bioacoustics data schema. It wraps [rclone](https://rclone.org) (embedded as a Go library, not a bundled/shelled-out binary) so that colleagues who aren't comfortable with the CLI can:

- back up one experiment at a time between two storage locations (never the whole multi-terabyte `experiments/` tree at once - that's what made the old hand-typed `rclone copy` commands slow), or
- download a sub-path of one experiment (a single deployment date, a single recorder) into a working folder, e.g. for analysis in R,

with a mandatory scan before anything is copied, and never a `sync`/delete operation - only additive `copy`.

Nothing about a storage location (path, remote type, `experiments/` folder name, file filter) is hardcoded, so this works for other labs' schemas and storage setups too, as long as they follow the same `[location]/experiments/[experiment name]/...` shape.

## Install

[![Download for macOS](https://img.shields.io/badge/Download-macOS-000?style=for-the-badge&logo=apple&logoColor=white)](https://github.com/OSU-Bee-Lab/FileSync/releases/latest/download/FileSync-macOS.zip) [![Download for Windows](https://img.shields.io/badge/Download-Windows-0078D6?style=for-the-badge&logo=windows&logoColor=white)](https://github.com/OSU-Bee-Lab/FileSync/releases/latest/download/FileSync-Windows.exe) [![Download for Linux](https://img.shields.io/badge/Download-Linux-FCC624?style=for-the-badge&logo=linux&logoColor=black)](https://github.com/OSU-Bee-Lab/FileSync/releases/latest/download/FileSync-Linux.tar.xz)

macOS users will have to fight against Gatekeeper to open FileSync. More on that [below](#macos--windows-woes).

## Overview

FileSync follows a Location-based workflow. Add each storage root you use (a local folder/drive, or a remote like SharePoint/OneDrive, Google Drive, Dropbox, or S3) once, then pick a source and destination whenever you want to move data. A mandatory scan always runs before any copy, so you see exactly what will move before it does.

1.  **Manage Locations** - add each storage root you use. For a remote, the wizard drives rclone's own OAuth sign-in flow and opens your browser automatically.
2.  **Backup / Sync** - pick a source and destination Location, select one or more experiments, scan, confirm, copy.
3.  **Download** - pick a source Location, drill into its `experiments/` tree to whatever depth you want (a whole experiment, one date, one recorder), choose any local folder as the destination, scan, confirm. Files land at `<destination>/<the folder you picked>/...`, preserving that structure rather than dumping everything flat into the destination root.

Locations and their filter/mtime defaults are saved per-machine (`~/Library/Application Support/FileSync/config.json` on macOS, `~/.config/FileSync/config.json` on Linux, `%AppData%\FileSync\config.json` on Windows). Remote credentials are saved in rclone's own default config file, untouched - if you also have the real `rclone` CLI installed, it will see the same remotes FileSync creates.

## Features

- Mandatory scan-then-confirm before every copy - you always see the file list before anything moves.
- Never deletes: every copy is additive (rclone `copy`, never `sync`), on both the sync and download screens.
- Multi-destination sync - fan a backup out to several Locations (local and cloud) from one scan.
- Per-file live progress on both the current transfer and the upload queue, with bounded concurrency and automatic retry on failure.
- Recorder-aware sync (Sony ICD-PX370, Olympus VN-541PC) with optional auto-delete-after-verify to reset a recorder for reuse in the field.
- Works with any storage layout following `[location]/experiments/[name]/...`
  - not hardcoded to the Bee Lab's own folders.

## macOS & Windows woes

Neither release is code-signed - there's no code-signing budget behind this app - so both OSes will flag the first launch. This doesn't mean anything is wrong, just click through it once:

**macOS (Sequoia and later):** double-clicking will be blocked outright. Go to **System Settings → Privacy & Security**, scroll to the bottom, and click **"Open Anyway"** next to the FileSync mention, then confirm once more. (Older macOS versions may instead offer right-click → Open as a shortcut - that works too.)

**Windows:** SmartScreen will show "Windows protected your PC". Click **"More info"**, then **"Run anyway"**.

This only needs doing once per machine per build - most friction disappears entirely if the app is shared over a local network drive/USB rather than downloaded through a browser, since the OS only flags files that came from an actual internet download.