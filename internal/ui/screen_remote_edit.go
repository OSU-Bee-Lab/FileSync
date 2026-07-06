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

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
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
			name := strings.TrimSpace(nameEntry.Text)
			if name == "" {
				dialog.ShowInformation("Name required", "Give this location a name first.", s.win)
				return
			}
			if localPath == "" {
				dialog.ShowInformation("Folder required", "Choose a local folder first.", s.win)
				return
			}
			s.cfg.Locations[id].Name = name
			s.cfg.Locations[id].RootPath = localPath
			s.saveConfig()
			showLocations(s)
		}

		body = container.NewVBox(
			widget.NewForm(&widget.FormItem{Text: "Name", Widget: nameEntry}),
			widget.NewForm(&widget.FormItem{Text: "Folder", Widget: container.NewBorder(nil, nil, chooseFolderBtn, nil, pathLabel)}),
		)
	} else {
		bt, currentFields, err := syncengine.RemoteConfig(loc.RemoteName)
		if err != nil {
			dialog.ShowError(fmt.Errorf("couldn't read remote settings: %w", err), s.win)
			showLocations(s)
			return
		}

		remotePathEntry := widget.NewEntry()
		remotePathEntry.SetPlaceHolder("Path within remote (blank = root)")
		remotePathEntry.SetText(loc.RootPath)
		browseRemoteBtn := widget.NewButton("Browse...", func() {
			browseRemoteSetup(s, loc.RemoteName, strings.TrimSpace(remotePathEntry.Text), nil, func(_ syncengine.DriveInfo, relPath string) {
				remotePathEntry.SetText(relPath)
			})
		})

		fieldWidgets := map[string]fyne.CanvasObject{}
		remoteFieldsBox := container.NewVBox()
		advancedFieldsBox := container.NewVBox()
		advancedAccordion := widget.NewAccordion(widget.NewAccordionItem("Advanced options", advancedFieldsBox))
		populateRemoteFields(s, bt, currentFields, remoteFieldsBox, advancedFieldsBox, fieldWidgets)

		if oauthBackends[bt] {
			showReauth = true
		}

		saveBtn.OnTapped = func() {
			name := strings.TrimSpace(nameEntry.Text)
			if name == "" {
				dialog.ShowInformation("Name required", "Give this location a name first.", s.win)
				return
			}

			specs, err := syncengine.FieldsFor(bt)
			if err != nil {
				dialog.ShowError(err, s.win)
				return
			}
			fields := map[string]string{}
			changed := false
			for _, f := range specs {
				w, ok := fieldWidgets[f.Key]
				if !ok {
					continue
				}
				v := fieldText(w)
				if f.IsSecret && v == "" {
					// Blank means "leave the existing credential alone" -
					// UpdateRemote only touches keys present in the map.
					continue
				}
				fields[f.Key] = v
				if f.IsSecret || currentFields[f.Key] != v {
					// A typed secret is always a change (there's nothing to
					// compare it to - currentFields never holds secrets).
					changed = true
				}
			}

			if !changed {
				// No backend settings actually changed, so there's no reason
				// to touch the remote at all - just re-authorizing on every
				// save (even a pure rename) would force a needless browser
				// round-trip.
				s.cfg.Locations[id].Name = name
				s.cfg.Locations[id].RootPath = strings.TrimSpace(remotePathEntry.Text)
				s.saveConfig()
				showLocations(s)
				return
			}

			saveBtn.Disable()
			runRemoteOAuthUpdate(s, "Saving...", "Updating "+name+"...", loc.RemoteName, fields, func(err error) {
				saveBtn.Enable()
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					dialog.ShowError(fmt.Errorf("couldn't update remote: %w", err), s.win)
					return
				}
				s.cfg.Locations[id].Name = name
				s.cfg.Locations[id].RootPath = strings.TrimSpace(remotePathEntry.Text)
				s.saveConfig()
				showLocations(s)
			})
		}

		fieldsArea := container.NewVBox(
			widget.NewForm(&widget.FormItem{Text: "Path within remote", Widget: container.NewBorder(nil, nil, nil, browseRemoteBtn, remotePathEntry)}),
			remoteFieldsBox,
		)
		if len(advancedFieldsBox.Objects) > 0 {
			fieldsArea.Add(advancedAccordion)
		}
		body = container.NewVBox(
			widget.NewForm(&widget.FormItem{Text: "Name", Widget: nameEntry}),
			fieldsArea,
		)
	}

	saveBtn.Importance = widget.HighImportance

	buttons := container.NewHBox(saveBtn, backBtn)
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

	content := container.NewBorder(
		widget.NewLabelWithStyle("Edit Location", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		buttons,
		nil, nil,
		// NewVScroll forces content to the window width (entries fill, no
		// horizontal scrollbar). It reports content min width to the window, so
		// keep every child narrow - long path labels are truncated and the
		// path-entry placeholder is short - to avoid stretching the window
		// across multiple monitors.
		container.NewVScroll(body),
	)
	s.setContent(container.NewPadded(content))
}
