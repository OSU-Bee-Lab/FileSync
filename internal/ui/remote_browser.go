package ui

import (
	"context"
	"path"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
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
	upBtn := widget.NewButton("⬆ Up", nil)

	itemsBox := container.NewVBox()
	scroll := container.NewVScroll(itemsBox)
	scroll.SetMinSize(fyne.NewSize(400, 320))

	var d dialog.Dialog
	useBtn := widget.NewButton("Use this folder", nil)
	useBtn.Importance = widget.HighImportance

	var refresh func()

	showDriveList := func() {
		pathLabel.SetText("Choose a drive / document library:")
		upBtn.Disable()
		useBtn.Disable()
		itemsBox.Objects = nil
		for _, dr := range drives {
			dr := dr
			label := dr.Name
			if dr.Type != "" {
				label += "  (" + dr.Type + ")"
			}
			b := widget.NewButton("🗂  "+label, func() {
				drive = dr
				atDrives = false
				current = startPath
				startPath = ""
				refresh()
			})
			b.Alignment = widget.ButtonAlignLeading
			itemsBox.Add(b)
		}
		itemsBox.Refresh()
	}

	showFolder := func() {
		useBtn.Enable()
		// Up returns to the drive list from a drive's root (when there is a
		// list to return to), otherwise climbs one path level.
		if current == "" && len(drives) <= 1 {
			upBtn.Disable()
		} else {
			upBtn.Enable()
		}
		loc := current
		if drive.Name != "" {
			loc = drive.Name + "/" + current
		}
		if current == "" && drive.Name == "" {
			loc = "(remote root)"
		}
		pathLabel.SetText("📁 " + strings.TrimRight(loc, "/"))

		itemsBox.Objects = nil
		itemsBox.Add(widget.NewLabel("Loading..."))
		itemsBox.Refresh()
		go func() {
			dirs, err := syncengine.ListRemoteDirsOnDrive(context.Background(), remoteName, drive, current)
			fyne.Do(func() {
				if err != nil {
					// Auth failures need the shared Reconnect window; other
					// errors (e.g. a pre-filled path that doesn't resolve in
					// this drive) shouldn't kill the browse - show them inline
					// and let the user navigate with Up/back.
					if isAuthError(err) {
						d.Hide()
						showLocationError(s, err, syncengine.Location{
							Kind:       syncengine.LocationRemote,
							Name:       remoteName,
							RemoteName: remoteName,
						})
						return
					}
					itemsBox.Objects = nil
					msg := widget.NewLabel("Couldn't open this folder:\n" + err.Error())
					msg.Wrapping = fyne.TextWrapWord
					itemsBox.Add(msg)
					itemsBox.Refresh()
					return
				}
				itemsBox.Objects = nil
				if len(dirs) == 0 {
					itemsBox.Add(widget.NewLabel("(no sub-folders here)"))
				}
				for _, name := range dirs {
					name := name
					b := widget.NewButton("📁 "+name, func() {
						current = path.Join(current, name)
						refresh()
					})
					b.Alignment = widget.ButtonAlignLeading
					itemsBox.Add(b)
				}
				itemsBox.Refresh()
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

	upBtn.OnTapped = func() {
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

	header := container.NewBorder(nil, nil, upBtn, nil, pathLabel)
	body := container.NewBorder(header, useBtn, nil, nil, scroll)

	d = dialog.NewCustom("Browse "+remoteName, "Cancel", body, s.win)
	d.Resize(fyne.NewSize(480, 500))
	d.Show()
	refresh()
}
