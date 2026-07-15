package ui

import (
	"context"
	"path"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// browseRemoteSetup is the one folder picker for remotes. It drills into an
// rclone remote a shallow level at a time (never recursive) and lets the user
// confirm an exact location with "Use this folder".
//
// When drives is non-empty (a freshly-authorized OneDrive/SharePoint remote
// that exposes several document libraries), the top level of the browse is
// the drive list: the user picks a library, then its folders - so choosing
// the drive and the path within it is one continuous flow they can verify by
// eye, instead of committing to a drive blind. Selecting a drive lists its
// contents live via a connection-string override, so nothing is written to
// the remote until "Use this folder". onConfirm then reports the chosen drive
// (zero DriveInfo when drives was empty) and the path relative to that drive.
//
// When drives is empty (editing an already-configured remote, or a backend
// with no drive concept), it browses the remote's own root from start.
func browseRemoteSetup(s *state, remoteName, start string, drives []syncengine.DriveInfo, onConfirm func(d syncengine.DriveInfo, relPath string)) {
	var drive syncengine.DriveInfo
	atDrives := false
	// startPath is applied as the initial folder once a drive is in play
	// (immediately for a single/zero-drive remote, or when the user picks a
	// drive from the list) - this is how a folder parsed out of a pasted
	// SharePoint URL pre-positions the browse. It's consumed once so later
	// navigation isn't overridden.
	startPath := strings.Trim(start, "/")
	current := startPath
	switch {
	case len(drives) > 1:
		atDrives = true
		current = ""
	case len(drives) == 1:
		drive = drives[0]
		startPath = ""
	}

	pathLabel := widget.NewLabel("")
	pathLabel.Wrapping = fyne.TextWrapWord
	backBtn := widget.NewButtonWithIcon("", theme.NavigateBackIcon(), nil)
	statusLbl := widget.NewLabel("")
	statusLbl.Wrapping = fyne.TextWrapWord
	loading := newLoadingBar()

	// rows mirrors destFolderBrowser's row model: drive/folder names to
	// browse into, one row per name, rendered via a widget.List so this
	// browser looks and behaves like the recorder-sync destination browser
	// (dest_folder_browser.go) rather than a bespoke stack of buttons.
	var rows []string
	var d dialog.Dialog
	useBtn := widget.NewButton("Use this folder", nil)
	useBtn.Importance = widget.HighImportance

	var refresh func()

	list := widget.NewList(
		func() int { return len(rows) },
		func() fyne.CanvasObject {
			b := widget.NewButton("", nil)
			b.Alignment = widget.ButtonAlignLeading
			return b
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			b := obj.(*widget.Button)
			name := rows[id]
			if atDrives {
				dr := drives[id]
				label := dr.Name
				if dr.Type != "" {
					label += "  (" + dr.Type + ")"
				}
				b.SetText("\U0001F5C2  " + label)
				b.OnTapped = func() {
					drive = dr
					atDrives = false
					current = startPath
					startPath = ""
					refresh()
				}
				return
			}
			b.SetText("\U0001F4C1 " + name)
			b.OnTapped = func() {
				current = path.Join(current, name)
				refresh()
			}
		},
	)
	scroll := container.NewVScroll(list)
	scroll.SetMinSize(fyne.NewSize(400, 320))

	showDriveList := func() {
		pathLabel.SetText("Choose a drive / document library:")
		backBtn.Disable()
		useBtn.Disable()
		statusLbl.SetText("")
		rows = make([]string, len(drives))
		for i, dr := range drives {
			rows[i] = dr.Name
		}
		list.Refresh()
	}

	showFolder := func() {
		useBtn.Enable()
		// Back returns to the drive list from a drive's root (when there is
		// a list to return to), otherwise climbs one path level.
		if current == "" && len(drives) <= 1 {
			backBtn.Disable()
		} else {
			backBtn.Enable()
		}
		loc := current
		if drive.Name != "" {
			loc = drive.Name + "/" + current
		}
		if current == "" && drive.Name == "" {
			loc = "(remote root)"
		}
		pathLabel.SetText("📁 " + strings.TrimRight(loc, "/"))

		rows = nil
		list.Refresh()
		statusLbl.SetText("")
		loading.Show()
		go func() {
			dirs, err := syncengine.ListRemoteDirsOnDrive(context.Background(), remoteName, drive, current)
			fyne.Do(func() {
				loading.Hide()
				if err != nil {
					// Auth failures need the shared Reconnect window; other
					// errors (e.g. a pre-filled path that doesn't resolve in
					// this drive) shouldn't kill the browse - show them
					// inline and let the user navigate with Back.
					if isAuthError(err) {
						d.Hide()
						showLocationError(s, err, syncengine.Location{
							Kind:       syncengine.LocationRemote,
							Name:       remoteName,
							RemoteName: remoteName,
						})
						return
					}
					statusLbl.SetText("Couldn't open this folder:\n" + err.Error())
					rows = nil
					list.Refresh()
					return
				}
				if len(dirs) == 0 {
					statusLbl.SetText("(no sub-folders here)")
				} else {
					statusLbl.SetText("")
				}
				rows = dirs
				list.Refresh()
			})
		}()
	}

	refresh = func() {
		if atDrives {
			showDriveList()
		} else {
			showFolder()
		}
	}

	backBtn.OnTapped = func() {
		if current == "" {
			if len(drives) > 1 {
				atDrives = true
				refresh()
			}
			return
		}
		current = path.Dir(current)
		if current == "." {
			current = ""
		}
		refresh()
	}

	useBtn.OnTapped = func() {
		onConfirm(drive, current)
		d.Hide()
	}
	cancelBtn := widget.NewButton("Cancel", func() { d.Hide() })

	header := container.NewBorder(nil, nil, backBtn, nil, pathLabel)
	footer := container.NewVBox(loading.CanvasObject(), statusLbl, container.NewCenter(container.NewHBox(useBtn, cancelBtn)))
	body := container.NewBorder(header, footer, nil, nil, scroll)

	d = dialog.NewCustomWithoutButtons("Browse "+remoteName, body, s.win)
	d.Resize(fyne.NewSize(480, 500))
	d.Show()
	refresh()
}
