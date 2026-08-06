package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// showEditLocation lets the user rename a location and, for remote
// locations, change its remote path or backend-specific settings (e.g.
// rotate an S3 key, fix a wrong endpoint) without deleting and re-adding
// it. Local locations can only have their name and folder changed here -
// there's no remote config to edit.
func showEditLocation(s *state, id int) {
	loc := s.cfg.Locations[id]

	nameEntry := widget.NewEntry()
	nameEntry.SetText(loc.Name)

	roleSelect := widget.NewSelect(roleLabels, nil)
	roleSelect.SetSelected(labelFromRole(loc.Role))

	saveBtn := widget.NewButton("Save", nil)
	backBtn := widget.NewButton("Cancel", func() { showLocations(s) })

	var body *fyne.Container
	showReauth := false

	if loc.Kind == syncengine.LocationLocal {
		localPath := loc.RootPath
		pathLabel := widget.NewLabel(localPath)
		// Long absolute paths otherwise force the label (and thus the whole
		// window) to their full pixel width - which on multi-monitor setups
		// stretches the window across displays. Truncation caps the min width.
		pathLabel.Truncation = fyne.TextTruncateEllipsis
		chooseFolderBtn := widget.NewButton("Choose folder...", func() {
			chooseFolder(s.win, func(path string, err error) {
				if err != nil {
					dialog.ShowError(err, s.win)
					return
				}
				if path == "" {
					return
				}
				localPath = path
				pathLabel.SetText(localPath)
			})
		})

		saveBtn.OnTapped = func() {
			if !requireNonEmpty(s.win, nameEntry.Text, "Nickname required", "Give this location a nickname first.") {
				return
			}
			name := strings.TrimSpace(nameEntry.Text)
			if !requireUniqueLocationName(s.win, s.cfg.Locations, name, loc.ID) {
				return
			}
			if !requireNonEmpty(s.win, localPath, "Folder required", "Choose a local folder first.") {
				return
			}
			s.cfg.Locations[id].Name = name
			s.cfg.Locations[id].RootPath = localPath
			s.cfg.Locations[id].Role = roleFromLabel(roleSelect.Selected)
			s.saveConfig()
			showLocations(s)
		}

		body = container.NewVBox(
			widget.NewForm(&widget.FormItem{Text: "Nickname", Widget: nameEntry}),
			widget.NewForm(&widget.FormItem{Text: "Folder", Widget: container.NewBorder(nil, nil, chooseFolderBtn, nil, pathLabel)}),
			widget.NewForm(&widget.FormItem{Text: "Role", Widget: roleSelect}),
		)
	} else {
		bt, currentFields, err := syncengine.RemoteConfig(loc.RemoteName)
		if err != nil {
			dialog.ShowError(fmt.Errorf("couldn't read remote settings: %w", err), s.win)
			showLocations(s)
			return
		}

		// form is the shared "Path within remote" + per-backend fields
		// scaffold (see remote_fields_form.go), prefilled with the remote's
		// current settings so readFields can tell an actual change from a
		// pure rename.
		form := newRemoteFieldsForm(s, bt, currentFields)
		form.pathEntry.SetPlaceHolder("Path within remote (blank = root)")
		form.pathEntry.SetText(loc.RootPath)
		form.browseBtn.OnTapped = func() {
			browseRemoteSetup(s, loc.RemoteName, strings.TrimSpace(form.pathEntry.Text), nil, func(_ syncengine.DriveInfo, relPath string) {
				form.pathEntry.SetText(relPath)
			})
		}

		if oauthBackends[bt] {
			showReauth = true
		}

		// siteURLEntry lets a SharePoint/OneDrive location show (and fix, if
		// it's gone stale) the site URL the user originally pasted in when
		// setting it up. rclone itself never stores this - it resolves
		// straight to drive_id/drive_type - so it's tracked separately on
		// the Location (SharePointSiteURL) rather than as an rclone field.
		var siteURLEntry *widget.Entry
		if bt == syncengine.BackendOneDrive {
			siteURLEntry = widget.NewEntry()
			siteURLEntry.SetPlaceHolder("SharePoint site URL")
			siteURLEntry.SetText(loc.SharePointSiteURL)
		}

		saveBtn.OnTapped = func() {
			if !requireNonEmpty(s.win, nameEntry.Text, "Nickname required", "Give this location a nickname first.") {
				return
			}
			name := strings.TrimSpace(nameEntry.Text)
			if !requireUniqueLocationName(s.win, s.cfg.Locations, name, loc.ID) {
				return
			}

			specs, err := syncengine.FieldsFor(bt)
			if err != nil {
				dialog.ShowError(err, s.win)
				return
			}
			fields, changed := form.readFields(specs)

			if !changed {
				// No backend settings actually changed, so there's no reason
				// to touch the remote at all - just re-authorizing on every
				// save (even a pure rename) would force a needless browser
				// round-trip.
				s.cfg.Locations[id].Name = name
				s.cfg.Locations[id].RootPath = strings.TrimSpace(form.pathEntry.Text)
				s.cfg.Locations[id].Role = roleFromLabel(roleSelect.Selected)
				if siteURLEntry != nil {
					s.cfg.Locations[id].SharePointSiteURL = strings.TrimSpace(siteURLEntry.Text)
				}
				s.saveConfig()
				showLocations(s)
				return
			}

			saveBtn.Disable()
			runRemoteOAuthUpdate(s, bt, "Saving...", "Updating "+name+"...", loc.RemoteName, fields, func(err error) {
				saveBtn.Enable()
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					dialog.ShowError(fmt.Errorf("couldn't update remote: %w", err), s.win)
					return
				}
				s.cfg.Locations[id].Name = name
				s.cfg.Locations[id].RootPath = strings.TrimSpace(form.pathEntry.Text)
				s.cfg.Locations[id].Role = roleFromLabel(roleSelect.Selected)
				if siteURLEntry != nil {
					s.cfg.Locations[id].SharePointSiteURL = strings.TrimSpace(siteURLEntry.Text)
				}
				s.saveConfig()
				showLocations(s)
			})
		}

		fieldsArea := container.NewVBox(
			form.pathRow(),
			form.container,
		)
		formRows := []fyne.CanvasObject{
			widget.NewForm(&widget.FormItem{Text: "Nickname", Widget: nameEntry}),
			widget.NewForm(&widget.FormItem{Text: "Role", Widget: roleSelect}),
		}
		if siteURLEntry != nil {
			formRows = append(formRows, widget.NewForm(&widget.FormItem{Text: "Site URL", Widget: siteURLEntry}))
		}
		formRows = append(formRows, fieldsArea)
		body = container.NewVBox(formRows...)
	}

	saveBtn.Importance = widget.HighImportance

	buttons := actionRow(backBtn, saveBtn)
	// OAuth remotes can have their browser sign-in expire independently of any
	// field change, so offer a dedicated re-authorize action (same path as the
	// Reconnect prompt) rather than making the user tweak a field to trigger
	// "Save & Re-authorize".
	if showReauth {
		reauthBtn := widget.NewButton("Re-authorize", func() {
			reconnectRemote(s, loc.RemoteName, loc.Name)
		})
		buttons.Add(reauthBtn)
	}

	// NewVScroll forces content to the window width (entries fill, no
	// horizontal scrollbar). It reports content min width to the window, so
	// keep every child narrow - long path labels are truncated and the
	// path-entry placeholder is short - to avoid stretching the window
	// across multiple monitors.
	scroll := container.NewVScroll(body)
	fixEntryScrolling(body, scroll)

	content := container.NewBorder(
		widget.NewLabelWithStyle("Edit Location", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		buttons,
		nil, nil,
		scroll,
	)
	s.setContent(centerMaxWidth(container.NewPadded(content), windowSize.Width))
}
