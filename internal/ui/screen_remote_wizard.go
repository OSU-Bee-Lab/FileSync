package ui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

var kindLabels = []string{
	"Local folder",
	"SharePoint / OneDrive",
	"Google Drive",
	"Dropbox",
	"S3-compatible",
}

var kindBackends = map[string]syncengine.BackendType{
	"SharePoint / OneDrive": syncengine.BackendOneDrive,
	"Google Drive":          syncengine.BackendDrive,
	"Dropbox":               syncengine.BackendDropbox,
	"S3-compatible":         syncengine.BackendS3,
}

// oauthBackends need a browser sign-in during CreateRemote, so their wizard
// button reads "Authorize" rather than "Save" - clicking it doesn't just
// persist a form, it kicks the user out to their browser.
var oauthBackends = map[syncengine.BackendType]bool{
	syncengine.BackendOneDrive: true,
	syncengine.BackendDrive:    true,
	syncengine.BackendDropbox:  true,
}

var remoteNameSanitizer = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func showAddLocation(s *state) {
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("e.g. Lab Server, OSU SharePoint")

	kindSelect := widget.NewSelect(kindLabels, nil)
	kindSelect.SetSelected(kindLabels[0])

	dynamicArea := container.NewVBox()

	// --- local folder state ---
	var localPath string
	localPathLabel := widget.NewLabel("No folder chosen")
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
			localPathLabel.SetText(localPath)
		})
	})

	// --- remote backend state ---
	remotePathEntry := widget.NewEntry()
	remotePathEntry.SetPlaceHolder("Path within the remote, e.g. \"Bee Lab Docs\" (leave blank for root)")

	siteURLEntry := widget.NewEntry()
	siteURLEntry.SetPlaceHolder("e.g. https://contoso.sharepoint.com/sites/mysite (leave blank for personal/business OneDrive)")

	// fieldWidgets holds either a *widget.Entry or *widget.Select per key,
	// so Save can read whichever one rebuildFields chose to render.
	fieldWidgets := map[string]fyne.CanvasObject{}
	remoteFieldsBox := container.NewVBox()
	advancedFieldsBox := container.NewVBox()
	advancedAccordion := widget.NewAccordion(widget.NewAccordionItem("Advanced options", advancedFieldsBox))

	rebuildFields := func(bt syncengine.BackendType) {
		populateRemoteFields(s, bt, nil, remoteFieldsBox, advancedFieldsBox, fieldWidgets)
	}

	saveBtn := widget.NewButton("Save", nil)

	rebuild := func() {
		dynamicArea.Objects = nil
		kind := kindSelect.Selected
		if kind == kindLabels[0] {
			dynamicArea.Add(container.NewHBox(chooseFolderBtn, localPathLabel))
			saveBtn.SetText("Save")
		} else {
			if kind == "SharePoint / OneDrive" {
				dynamicArea.Add(widget.NewForm(&widget.FormItem{Text: "SharePoint site URL", Widget: siteURLEntry}))
			}
			dynamicArea.Add(widget.NewForm(&widget.FormItem{Text: "Path within remote", Widget: remotePathEntry}))
			dynamicArea.Add(remoteFieldsBox)
			rebuildFields(kindBackends[kind])
			if len(advancedFieldsBox.Objects) > 0 {
				dynamicArea.Add(advancedAccordion)
			}
			if oauthBackends[kindBackends[kind]] {
				saveBtn.SetText("Authorize")
			} else {
				saveBtn.SetText("Save")
			}
		}
		dynamicArea.Refresh()
	}
	kindSelect.OnChanged = func(string) { rebuild() }
	rebuild()

	saveBtn.OnTapped = func() {
		name := strings.TrimSpace(nameEntry.Text)
		if name == "" {
			dialog.ShowInformation("Name required", "Give this location a name first.", s.win)
			return
		}
		if kindSelect.Selected == kindLabels[0] {
			if localPath == "" {
				dialog.ShowInformation("Folder required", "Choose a local folder first.", s.win)
				return
			}
			s.cfg.Locations = append(s.cfg.Locations, syncengine.Location{
				ID:       newLocationID(),
				Name:     name,
				Kind:     syncengine.LocationLocal,
				RootPath: localPath,
			})
			s.saveConfig()
			showLocations(s)
			return
		}

		bt := kindBackends[kindSelect.Selected]
		fields := map[string]string{}
		for k, w := range fieldWidgets {
			fields[k] = fieldText(w)
		}
		if bt == syncengine.BackendOneDrive {
			fields[syncengine.SharePointSiteURLKey] = strings.TrimSpace(siteURLEntry.Text)
		}
		remoteName := remoteNameSanitizer.ReplaceAllString(name, "-")

		saveBtn.Disable()
		progressLabel := widget.NewLabel("Setting up " + kindSelect.Selected + "...")
		progressDialog := dialog.NewCustom("Connecting...", "Please wait", progressLabel, s.win)
		progressDialog.Show()

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			err := syncengine.CreateRemote(ctx, remoteName, bt, fields, func(url string) {
				fyne.Do(func() {
					progressLabel.SetText("Opening your browser to sign in...\nIf it doesn't open, visit:\n" + url)
				})
			})
			fyne.Do(func() {
				progressDialog.Hide()
				saveBtn.Enable()
				if err != nil {
					dialog.ShowError(fmt.Errorf("couldn't set up remote: %w", err), s.win)
					return
				}
				s.cfg.Locations = append(s.cfg.Locations, syncengine.Location{
					ID:         newLocationID(),
					Name:       name,
					Kind:       syncengine.LocationRemote,
					RemoteName: remoteName,
					RootPath:   strings.TrimSpace(remotePathEntry.Text),
				})
				s.saveConfig()
				showLocations(s)
			})
		}()
	}
	saveBtn.Importance = widget.HighImportance

	form := container.NewVBox(
		widget.NewForm(&widget.FormItem{Text: "Name", Widget: nameEntry}),
		widget.NewForm(&widget.FormItem{Text: "Type", Widget: kindSelect}),
		dynamicArea,
	)

	backBtn := widget.NewButton("Cancel", func() { showLocations(s) })
	content := container.NewBorder(
		widget.NewLabelWithStyle("Add Location", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(saveBtn, backBtn),
		nil, nil,
		container.NewVScroll(form),
	)
	s.setContent(container.NewPadded(content))
}

// fieldText reads back whichever widget type populateRemoteFields chose to
// render for a given FieldSpec.
func fieldText(w fyne.CanvasObject) string {
	switch e := w.(type) {
	case *widget.Entry:
		return e.Text
	case *widget.Select:
		return e.Selected
	}
	return ""
}

// populateRemoteFields renders the FieldSpecs for bt into remoteFieldsBox /
// advancedFieldsBox and records the widget for each field's key in
// fieldWidgets (cleared first), so callers can read values back later via
// fieldText. prefill overrides a field's rclone-reported default when
// present - used by the edit screen to show a remote's current values;
// showAddLocation passes nil so every field starts at its backend default.
// Shared between showAddLocation and showEditLocation so the two forms
// never drift apart in how they build backend-specific fields.
func populateRemoteFields(s *state, bt syncengine.BackendType, prefill map[string]string, remoteFieldsBox, advancedFieldsBox *fyne.Container, fieldWidgets map[string]fyne.CanvasObject) {
	remoteFieldsBox.Objects = nil
	advancedFieldsBox.Objects = nil
	for k := range fieldWidgets {
		delete(fieldWidgets, k)
	}
	specs, err := syncengine.FieldsFor(bt)
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	for _, f := range specs {
		var w fyne.CanvasObject
		label := f.Label
		if f.Required {
			label += " *"
		}
		value := f.Default
		if v, ok := prefill[f.Key]; ok {
			value = v
		}
		if len(f.Choices) > 0 {
			sel := widget.NewSelect(f.Choices, nil)
			if value != "" {
				sel.SetSelected(value)
			}
			w = sel
		} else {
			e := widget.NewEntry()
			if f.IsSecret {
				e = widget.NewPasswordEntry()
				e.SetPlaceHolder("leave blank to keep existing value")
			} else {
				e.SetText(value)
			}
			w = e
		}
		fieldWidgets[f.Key] = w
		item := widget.NewForm(&widget.FormItem{Text: label, Widget: w, HintText: f.HelpText})
		if f.Advanced {
			advancedFieldsBox.Add(item)
		} else {
			remoteFieldsBox.Add(item)
		}
	}
	remoteFieldsBox.Refresh()
	advancedFieldsBox.Refresh()
}

var locationIDCounter int

// newLocationID mints a simple locally-unique ID. Not a UUID library
// dependency for the sake of one counter - IDs only need to be unique
// within this machine's own config file.
func newLocationID() string {
	locationIDCounter++
	return fmt.Sprintf("loc-%d-%d", time.Now().UnixNano(), locationIDCounter)
}
