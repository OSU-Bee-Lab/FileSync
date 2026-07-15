package ui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// kindLabels and kindBackends are derived from remoteBackendKinds (see
// remote_backend_kinds.go), the single source of truth for backend<->label
// mapping shared with screen_location_transfer.go.
var kindLabels = remoteKindLabels()
var kindBackends = remoteKindByLabel()

// oauthBackends need a browser sign-in during CreateRemote, so their wizard
// button reads "Authorize" rather than "Save" - clicking it doesn't just
// persist a form, it kicks the user out to their browser.
var oauthBackends = map[syncengine.BackendType]bool{
	syncengine.BackendOneDrive: true,
	syncengine.BackendDrive:    true,
	syncengine.BackendDropbox:  true,
}

var remoteNameSanitizer = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// backendsWithHiddenBasicFields are backends where every FieldSpec is tucked
// into the "Advanced options" accordion, regardless of rclone's own Advanced
// flag on each option. These backends already give the user a top-level way
// to point at the right place - SharePoint/OneDrive's site URL entry, and
// Google Drive/Dropbox's "Path within remote" + Browse row - so the
// remaining rclone options (client IDs, chunk sizes, etc.) are things almost
// no one needs to touch and would otherwise clutter the form.
var backendsWithHiddenBasicFields = map[syncengine.BackendType]bool{
	syncengine.BackendOneDrive: true,
	syncengine.BackendDrive:    true,
	syncengine.BackendDropbox:  true,
}

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
	// remoteReady tracks whether the rclone remote backing this new location
	// has already been created and authorized this session, so "Browse" and
	// "Save" don't each trigger a separate sign-in.
	var remoteReady bool
	var createdRemoteName string
	// capturedDrives holds the drives an OAuth remote offered at config time
	// (empty for single-drive or non-drive backends). driveConfirmed records
	// whether the user has settled on one (which SetRemoteDrive persists onto
	// the remote) before the location is saved.
	var capturedDrives []syncengine.DriveInfo
	var driveConfirmed bool

	siteURLEntry := widget.NewEntry()
	siteURLEntry.SetPlaceHolder("e.g. https://contoso.sharepoint.com/sites/mysite (leave blank for personal/business OneDrive)")

	// form is the shared "Path within remote" + per-backend fields scaffold
	// (see remote_fields_form.go). It's built once here (seeded with the
	// first remote kind) and updated in place via setBackend as the kind
	// picker changes, so form.pathEntry keeps whatever path the user already
	// typed across a kind switch, same as the old single remotePathEntry did.
	form := newRemoteFieldsForm(s, kindBackends[kindLabels[1]], nil)
	form.pathEntry.SetPlaceHolder("Path within the remote, e.g. \"Bee Lab Docs\" (leave blank for root)")

	saveBtn := widget.NewButton("Save", nil)

	rebuild := func() {
		dynamicArea.Objects = nil
		kind := kindSelect.Selected
		if kind == kindLabels[0] {
			dynamicArea.Add(container.NewHBox(chooseFolderBtn, localPathLabel))
			saveBtn.SetText("Save")
		} else if kind == "SharePoint / OneDrive" {
			// OneDrive/SharePoint: the user gives only the site URL (a folder
			// baked into a copied library URL is parsed out automatically).
			// "Next" authorizes then opens the browser to pick the exact
			// document library and folder - there's no path to type here.
			dynamicArea.Add(widget.NewForm(&widget.FormItem{Text: "SharePoint site URL", Widget: siteURLEntry}))
			form.setBackend(s, kindBackends[kind])
			dynamicArea.Add(form.container)
			saveBtn.SetText("Next")
		} else if oauthBackends[kindBackends[kind]] {
			// Google Drive/Dropbox: no free-typed path row either - "Next"
			// authorizes then opens the same folder browser OneDrive uses,
			// so there's never a hand-typed path to get wrong.
			form.setBackend(s, kindBackends[kind])
			dynamicArea.Add(form.container)
			saveBtn.SetText("Next")
		} else {
			form.setBackend(s, kindBackends[kind])
			dynamicArea.Add(form.pathRow())
			dynamicArea.Add(form.container)
			saveBtn.SetText("Save")
		}
		dynamicArea.Refresh()
	}
	kindSelect.OnChanged = func(string) { rebuild() }
	rebuild()

	// ensureRemote makes sure the rclone remote backing this new location
	// exists and is authorized, running the browser sign-in the first time it
	// is needed. Both "Browse" (to list the remote's folders) and "Save" need
	// a live remote, so they share this rather than each authorizing - and
	// once created, later calls reuse it instead of re-signing-in. onReady
	// runs on the UI goroutine once the remote is ready.
	ensureRemote := func(onReady func()) {
		if !requireNonEmpty(s.win, nameEntry.Text, "Name required", "Give this location a name first.") {
			return
		}
		if remoteReady {
			onReady()
			return
		}
		bt := kindBackends[kindSelect.Selected]
		specs, err := syncengine.FieldsFor(bt)
		if err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		fields, _ := form.readFields(specs)
		if bt == syncengine.BackendOneDrive {
			// A pasted library URL may carry a folder; only the clean site URL
			// steers rclone. The folder is handled separately (see Next).
			siteURL, _ := syncengine.ParseSharePointURL(siteURLEntry.Text)
			fields[syncengine.SharePointSiteURLKey] = siteURL
		}
		remoteName := remoteNameSanitizer.ReplaceAllString(strings.TrimSpace(nameEntry.Text), "-")

		// The drive picker doesn't prompt here: it just records the drives on
		// offer and picks the first so config can finish and yield a token.
		// The real choice is made afterwards in the browser (openBrowser),
		// which can list a drive's contents so the user picks by sight.
		capturedDrives = nil
		chooseDrive := func(drives []syncengine.DriveInfo) (syncengine.DriveInfo, error) {
			capturedDrives = drives
			return drives[0], nil
		}

		saveBtn.Disable()
		runRemoteOAuth(s, bt, "Connecting...", "Setting up "+kindSelect.Selected+"...",
			func(ctx context.Context, onAuthURL func(url string)) error {
				return syncengine.CreateRemote(ctx, remoteName, bt, fields, onAuthURL, chooseDrive)
			},
			func(err error) {
				saveBtn.Enable()
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					dialog.ShowError(fmt.Errorf("couldn't set up remote: %w", err), s.win)
					return
				}
				remoteReady = true
				createdRemoteName = remoteName
				// A single-drive (or driveless) remote needs no picking: its
				// one drive is already what config committed to.
				if len(capturedDrives) == 1 {
					driveConfirmed = true
				}
				onReady()
			})
	}

	// openBrowser drills into the remote (drive list first when several were
	// offered, then folders) so the user confirms an exact location. On
	// confirm it persists the chosen drive and fills in the path, then runs
	// then() - letting Save chain straight into saving the location.
	openBrowser := func(start string, then func()) {
		drives := capturedDrives
		if driveConfirmed {
			// Drive already settled - browse folders within it, no drive list.
			drives = nil
		}
		browseRemoteSetup(s, createdRemoteName, start, drives,
			func(d syncengine.DriveInfo, relPath string) {
				if d.ID != "" {
					if err := syncengine.SetRemoteDrive(createdRemoteName, d); err != nil {
						dialog.ShowError(fmt.Errorf("couldn't set drive: %w", err), s.win)
						return
					}
					driveConfirmed = true
				}
				form.pathEntry.SetText(relPath)
				if then != nil {
					then()
				}
			})
	}

	form.browseBtn.OnTapped = func() {
		ensureRemote(func() { openBrowser(strings.TrimSpace(form.pathEntry.Text), nil) })
	}

	saveBtn.OnTapped = func() {
		if !requireNonEmpty(s.win, nameEntry.Text, "Name required", "Give this location a name first.") {
			return
		}
		name := strings.TrimSpace(nameEntry.Text)
		if kindSelect.Selected == kindLabels[0] {
			if !requireNonEmpty(s.win, localPath, "Folder required", "Choose a local folder first.") {
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
		ensureRemote(func() {
			finalize := func() {
				s.cfg.Locations = append(s.cfg.Locations, syncengine.Location{
					ID:         newLocationID(),
					Name:       strings.TrimSpace(nameEntry.Text),
					Kind:       syncengine.LocationRemote,
					RemoteName: createdRemoteName,
					RootPath:   strings.TrimSpace(form.pathEntry.Text),
				})
				s.saveConfig()
				showLocations(s)
			}
			// OAuth backends (OneDrive/SharePoint, Google Drive, Dropbox):
			// "Next" always opens the browser to choose the exact folder
			// (pre-positioned at any folder parsed out of a pasted
			// SharePoint URL), then saves on confirm - there's no hand-typed
			// path row for these.
			if oauthBackends[bt] {
				start := ""
				if bt == syncengine.BackendOneDrive {
					_, start = syncengine.ParseSharePointURL(siteURLEntry.Text)
				}
				openBrowser(start, finalize)
				return
			}
			finalize()
		})
	}
	saveBtn.Importance = widget.HighImportance

	layout := container.NewVBox(
		widget.NewForm(&widget.FormItem{Text: "Name", Widget: nameEntry}),
		widget.NewForm(&widget.FormItem{Text: "Type", Widget: kindSelect}),
		dynamicArea,
	)

	backBtn := widget.NewButton("Cancel", func() { showLocations(s) })
	content := container.NewBorder(
		widget.NewLabelWithStyle("Add Location", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewHBox(saveBtn, backBtn),
		nil, nil,
		container.NewVScroll(layout),
	)
	s.setContent(container.NewPadded(content))
}

// populateRemoteFields renders the FieldSpecs for bt into remoteFieldsBox /
// advancedFieldsBox and records the widget for each field's key in
// fieldWidgets (cleared first), so callers can read values back later via
// fieldText. prefill overrides a field's rclone-reported default when
// present - used by the edit screen to show a remote's current values;
// showAddLocation passes nil so every field starts at its backend default.
// Shared between showAddLocation and showEditLocation (via remoteFieldsForm,
// see remote_fields_form.go) so the two forms never drift apart in how they
// build backend-specific fields.
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
		item := fyne.CanvasObject(widget.NewForm(&widget.FormItem{Text: label, Widget: w}))
		if f.HelpText != "" {
			// widget.Form's own HintText renders as an unwrapped canvas.Text,
			// whose MinSize tracks the full (sometimes 200+ char) rclone help
			// string - and even inside a collapsed "Advanced options"
			// accordion item, that width still counts toward the whole
			// screen's MinSize, stretching the window. A wrapped Label avoids
			// that: its min width is just its widest word.
			hint := widget.NewLabel(f.HelpText)
			hint.Wrapping = fyne.TextWrapWord
			hint.TextStyle = fyne.TextStyle{Italic: true}
			item = container.NewVBox(item, hint)
		}
		if f.Advanced || backendsWithHiddenBasicFields[bt] {
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
