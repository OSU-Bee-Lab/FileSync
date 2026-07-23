package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"path"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/rclone/rclone/fs"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// manageOpRetime is the radio label for the dev-gated "Retime" option (see
// devMode) - checking and correcting recorder clock errors already synced to
// disk, via the same recorder.CheckRecorderTimestamp/ApplyTimestampFix
// pathway and review screen Sync Recorders uses (see runManageFilesRetime),
// just scanning an arbitrary directory recursively instead of a live volume.
const manageOpRetime = "Retime (check recorder timestamps)"

var (
	manageColorDeleteBg = color.NRGBA{R: 0xFE, G: 0xCA, B: 0xCA, A: 0xFF} // red wash - permanently removed
	manageColorMoveBg   = color.NRGBA{R: 0xBF, G: 0xDB, B: 0xFE, A: 0xFF} // blue wash - TO (destination) rows
	manageColorFromBg   = color.NRGBA{R: 0xE5, G: 0xE7, B: 0xEB, A: 0xFF} // gray wash - FROM (source) rows
)

// commaInt formats n with thousands separators (e.g. 1234 -> "1,234"), for
// the irreversible-delete confirmation's file count.
func commaInt(n int) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// strikethroughText overlays a Unicode combining long-stroke on every
// rune, since fyne.TextStyle has no strikethrough of its own (only Bold/
// Italic/Underline as of fyne v2.7). Used to mark a move/rename's source
// path as "going away" without a second font-rendering path.
func strikethroughText(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(r)
		b.WriteRune('̶')
	}
	return b.String()
}

// focusEntry is a widget.Entry that reports focus changes, used so the
// shared path picker in Manage Files knows whether "From" or "To" is
// currently being edited (onFocus), and so "From" can validate its typed
// path once the user is done editing it rather than on every keystroke
// (onBlur).
type focusEntry struct {
	widget.Entry
	onFocus func()
	onBlur  func()
}

func newFocusEntry(onFocus, onBlur func()) *focusEntry {
	e := &focusEntry{onFocus: onFocus, onBlur: onBlur}
	e.ExtendBaseWidget(e)
	return e
}

func (e *focusEntry) FocusGained() {
	e.Entry.FocusGained()
	if e.onFocus != nil {
		e.onFocus()
	}
}

func (e *focusEntry) FocusLost() {
	e.Entry.FocusLost()
	if e.onBlur != nil {
		e.onBlur()
	}
}

// showManageFiles is the Manage Files flow: direct rename, move/merge, and
// delete operations against experiment data on one or more selected
// Locations. See CLAUDE.md's Manage Files exception — this is a deliberate,
// user-driven carve-out from the app's otherwise copy-only sync engine,
// requiring an explicit preview/collision-resolution/confirm sequence
// before anything is applied. Preview is not optional: pressing "Preview"
// always lands on showManageFilesPreview before anything can be applied, no matter which
// operation or how simple it looks.
func showManageFiles(s *state) {
	names := locationNames(s.cfg.Locations)
	locGroup := newToggleGroup(names, selectedFromIDs(s.cfg.Locations, s.cfg.ManageFilesLocationIDs))
	mirrorWarning := widget.NewLabel("")
	mirrorWarning.Wrapping = fyne.TextWrapWord

	updateMirrorWarning := func() {
		selected := locGroup.Selected()
		if len(selected) > 0 && len(selected) < len(s.cfg.Locations) {
			mirrorWarning.SetText("Warning: only " + fmt.Sprint(len(selected)) + " of " + fmt.Sprint(len(s.cfg.Locations)) +
				" Locations selected. Per SCHEMA.md, mirrored Locations should always change together — " +
				"applying this to only some of them will make the mirrors diverge.")
		} else {
			mirrorWarning.SetText("")
		}
	}

	opOptions := []string{"Rename / Move / Merge", "Delete", manageOpRetime}
	opGroup := widget.NewRadioGroup(opOptions, nil)

	// --- shared path picker: browses the first selected Location, and
	// writes into whichever of fromEntry/toEntry last had focus. Delete
	// only ever targets "From". ---
	deleteConfirmEntry := widget.NewEntry()
	deleteConfirmEntry.SetPlaceHolder("type the exact relative path to confirm")

	pickerTarget := "From" // "From" or "To" - which entry the picker/breadcrumb currently targets
	// pickerHeaderLabel replaces the usual static sectionHeader title with
	// the live "From"/"To" target, so the browser's own banner (not a
	// separate row beneath it) tells the user which field it's populating.
	pickerHeaderBg := canvas.NewRectangle(color.NRGBA{R: 240, G: 242, B: 245, A: 255})
	pickerHeaderLabel := widget.NewLabelWithStyle("From", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	pickerHeader := container.NewStack(pickerHeaderBg, container.NewPadded(pickerHeaderLabel))
	relPath := "" // path currently browsed in the picker
	breadcrumb := widget.NewLabel("experiments/")

	var fromEntry, toEntry *widget.Entry
	targetEntry := func() *widget.Entry {
		if pickerTarget == "To" {
			return toEntry
		}
		return fromEntry
	}

	// selectFile fills the current target (From/To) with a tapped file's
	// full relative path - unlike a directory tap, it never navigates the
	// picker (a file has no children to browse into). Assigned once
	// validateFromPath exists below; only ever called from the running UI,
	// well after that assignment happens.
	var selectFile func(relPath string)

	// addingFolder/addFolderText/needsFocus back the trailing "+ New Folder"
	// row, shown only while picking "To" - typing a destination that
	// doesn't exist yet is the point there (rclone creates it on Apply);
	// "From" must always be an existing path, so it never gets this row.
	// Same pattern as destFolderBrowser's own add-folder row.
	var entries []syncengine.Entry
	addingFolder := false
	addFolderText := ""
	needsFocus := false

	list := widget.NewList(
		func() int {
			if pickerTarget == "To" {
				return len(entries) + 1
			}
			return len(entries)
		},
		func() fyne.CanvasObject {
			bg := canvas.NewRectangle(color.Transparent)
			entry := widget.NewEntry()
			entry.Hide()
			return container.NewStack(bg, widget.NewButton("", nil), entry)
		},
		nil,
	)
	upBtn := widget.NewButton("Up", nil)

	refLoc := func() *syncengine.Location {
		sel := locGroup.Selected()
		if len(sel) == 0 {
			return nil
		}
		return findLocation(s.cfg.Locations, sel[0])
	}

	var loadChildren func()
	loadChildren = func() {
		loc := refLoc()
		if loc == nil {
			entries = nil
			list.Refresh()
			breadcrumb.SetText("Select a Location above first.")
			return
		}
		breadcrumb.SetText("Loading experiments/" + relPath + "...")
		src := *loc
		p := relPath
		target := pickerTarget
		go func() {
			result, err := syncengine.ListChildren(context.Background(), src, p)
			fyne.Do(func() {
				if relPath != p {
					return
				}
				if err != nil {
					// While picking "To", browsing to a folder that doesn't
					// exist yet is the expected way to name a new
					// destination (e.g. via "+ New Folder" or typing a name
					// that doesn't exist at any Location) - rclone creates
					// it on Apply, so show it as empty rather than erroring.
					// "From" must always be an existing path, but that's
					// surfaced by validateFromPath on blur (an inline
					// message), not a modal here - so it also just shows
					// empty rather than popping a dialog on every keystroke/
					// focus change. relPath naming an existing file (not a
					// directory - e.g. typed/submitted rather than tapped
					// from the list, which goes through selectFile instead)
					// is likewise not an error: a file just has no children,
					// so it browses as an empty listing rather than erroring.
					if errors.Is(err, fs.ErrorDirNotFound) || errors.Is(err, fs.ErrorIsFile) {
						entries = nil
						suffix := ""
						if target == "To" && errors.Is(err, fs.ErrorDirNotFound) {
							suffix = " (new folder)"
						}
						breadcrumb.SetText("experiments/" + relPath + suffix)
						upBtn.Disable()
						if relPath != "" {
							upBtn.Enable()
						}
						list.Refresh()
						return
					}
					breadcrumb.SetText("Error loading experiments/" + relPath)
					showLocationError(s, err, src)
					return
				}
				entries = result
				breadcrumb.SetText("experiments/" + relPath)
				upBtn.Disable()
				if relPath != "" {
					upBtn.Enable()
				}
				list.Refresh()
			})
		}()
	}

	closeAddFolder := func() {
		addingFolder = false
		addFolderText = ""
	}

	setRelPath := func(p string) {
		closeAddFolder()
		relPath = p
		targetEntry().SetText(p)
	}

	// commitNewFolder folds the typed name into relPath and re-opens
	// browsing under it - nothing is created on disk here; rclone copy/move
	// creates any missing destination directory itself once Apply runs.
	var commitNewFolder func()
	commitNewFolder = func() {
		name := strings.TrimSpace(addFolderText)
		closeAddFolder()
		if name == "" {
			list.Refresh()
			return
		}
		setRelPath(joinRel(relPath, name))
		loadChildren()
	}

	list.UpdateItem = func(id widget.ListItemID, obj fyne.CanvasObject) {
		stack := obj.(*fyne.Container)
		bg := stack.Objects[0].(*canvas.Rectangle)
		btn := stack.Objects[1].(*widget.Button)
		entry := stack.Objects[2].(*widget.Entry)

		if int(id) < len(entries) {
			entry.Hide()
			btn.Show()
			e := entries[id]
			label := e.Name
			if e.IsDir {
				label = "\U0001F4C1 " + label
				bg.FillColor = color.Transparent
				btn.Importance = widget.MediumImportance
			} else {
				label = fmt.Sprintf("%s  (%s)", label, humanBytes(e.Size))
				// Selected-file highlight: blue when this row's path is the
				// current target's (From/To) selection, matching the same
				// blue used for TO rows in the preview screen
				// (manageColorMoveBg). LowImportance so the button's own
				// (otherwise opaque) background doesn't paint over the tint
				// underneath it.
				entryPath := joinRel(relPath, e.Name)
				if entryPath == strings.Trim(strings.TrimSpace(targetEntry().Text), "/") {
					bg.FillColor = manageColorMoveBg
				} else {
					bg.FillColor = color.Transparent
				}
				btn.Importance = widget.LowImportance
			}
			bg.Refresh()
			btn.Refresh()
			btn.SetText(label)
			btn.OnTapped = func() {
				if e.IsDir {
					setRelPath(joinRel(relPath, e.Name))
					loadChildren()
					return
				}
				selectFile(joinRel(relPath, e.Name))
			}
			return
		}
		bg.FillColor = color.Transparent
		bg.Refresh()

		// Trailing "+ New Folder" row (only present while pickerTarget ==
		// "To" - see the list's Length func above).
		if addingFolder {
			btn.Hide()
			entry.Show()
			entry.SetText(addFolderText)
			entry.OnChanged = func(text string) { addFolderText = text }
			entry.OnSubmitted = func(string) { commitNewFolder() }
			if needsFocus {
				needsFocus = false
				s.win.Canvas().Focus(entry)
			}
			return
		}
		entry.Hide()
		btn.Show()
		btn.SetText("+ New Folder")
		btn.OnTapped = func() {
			addingFolder = true
			addFolderText = ""
			needsFocus = true
			list.Refresh()
		}
	}
	upBtn.OnTapped = func() {
		p := path.Dir(relPath)
		if p == "." {
			p = ""
		}
		setRelPath(p)
		loadChildren()
	}
	locGroup.OnChanged = func(sel []string) {
		s.cfg.ManageFilesLocationIDs = idsFromLocations(locationsFromNamesAny(s.cfg.Locations, sel))
		s.saveConfig()
		updateMirrorWarning()
		// Changing which Locations the operation applies to shouldn't
		// disturb the already-typed/browsed From/To paths - just re-browse
		// the (possibly new) reference Location at the current relPath.
		loadChildren()
	}

	setPickerTarget := func(target string) {
		pickerTarget = target
		closeAddFolder()
		pickerHeaderLabel.SetText(target)
		relPath = strings.Trim(strings.TrimSpace(targetEntry().Text), "/")
		breadcrumb.SetText("experiments/" + relPath)
		loadChildren()
	}

	// fromPathError surfaces a not-found "From" path inline, once the user
	// is done editing it (see validateFromPath) - never as a modal, and
	// never just from clicking into the field.
	fromPathError := widget.NewLabel("")
	fromPathError.Wrapping = fyne.TextWrapWord
	fromPathError.Hide()

	// validateFromPath checks the typed "From" path against every
	// currently selected Location (not just the picker's reference
	// Location), since the operation is meant to apply to all of them.
	validateFromPath := func() {
		p := strings.Trim(strings.TrimSpace(fromEntry.Text), "/")
		if p == "" {
			fromPathError.Hide()
			return
		}
		locs := locationsFromNamesAny(s.cfg.Locations, locGroup.Selected())
		if len(locs) == 0 {
			fromPathError.Hide()
			return
		}
		go func() {
			found := false
			for _, loc := range locs {
				// ListChildren errors with ErrorIsFile when p names a file
				// rather than a directory - the picker lets "From" select an
				// individual file, so that's a valid path too, not a miss.
				if _, err := syncengine.ListChildren(context.Background(), loc, p); err == nil || errors.Is(err, fs.ErrorIsFile) {
					found = true
					break
				}
			}
			fyne.Do(func() {
				if strings.Trim(strings.TrimSpace(fromEntry.Text), "/") != p {
					return // stale - the field changed since this check started
				}
				if found {
					fromPathError.Hide()
				} else {
					fromPathError.SetText("Path not found: this path is not present on any of the currently selected locations")
					fromPathError.Show()
				}
			})
		}()
	}

	selectFile = func(p string) {
		closeAddFolder()
		targetEntry().SetText(p)
		if pickerTarget == "From" {
			validateFromPath()
		}
		list.Refresh()
	}

	fromFocusEntry := newFocusEntry(func() { setPickerTarget("From") }, validateFromPath)
	fromFocusEntry.SetPlaceHolder("experiments/<relative path to rename/move/delete>")
	toFocusEntry := newFocusEntry(func() { setPickerTarget("To") }, nil)
	toFocusEntry.SetPlaceHolder("experiments/<new name or destination folder>")
	fromEntry = &fromFocusEntry.Entry
	toEntry = &toFocusEntry.Entry

	fromEntry.OnSubmitted = func(text string) {
		if pickerTarget == "From" {
			relPath = strings.Trim(strings.TrimSpace(text), "/")
			loadChildren()
		}
	}
	toEntry.OnSubmitted = func(text string) {
		if pickerTarget == "To" {
			relPath = strings.Trim(strings.TrimSpace(text), "/")
			loadChildren()
		}
	}

	// "From" is always shown; only the operation-specific second field
	// (move's "To", delete's confirm field) toggles with opGroup. Each
	// widget lives in exactly one form/container at a time.
	fromForm := widget.NewForm(widget.NewFormItem("From", fromFocusEntry))
	toForm := widget.NewForm(widget.NewFormItem("To", toFocusEntry))
	deleteForm := widget.NewForm(widget.NewFormItem("Confirm path", deleteConfirmEntry))
	toForm.Hide()
	deleteForm.Hide()

	opGroup.OnChanged = func(v string) {
		toForm.Hide()
		deleteForm.Hide()
		switch v {
		case "Rename / Move / Merge":
			toForm.Show()
		case "Delete":
			deleteForm.Show()
		}
	}
	opGroup.SetSelected("Rename / Move / Merge")

	backBtn := widget.NewButton("Back", func() { showHome(s) })

	previewBtn := widget.NewButton("Preview", func() {
		selectedNames := locGroup.Selected()
		if len(selectedNames) == 0 {
			dialog.ShowInformation("Select a Location", "Choose at least one Location to apply this operation to.", s.win)
			return
		}
		from := strings.Trim(strings.TrimSpace(fromEntry.Text), "/")
		if from == "" {
			dialog.ShowInformation("Missing path", "Pick or type a \"From\" path first.", s.win)
			return
		}
		op := opGroup.Selected
		locs := locationsFromNamesAny(s.cfg.Locations, selectedNames)

		if op == manageOpRetime {
			runManageFilesRetime(s, locs, from)
			return
		}

		if op == "Delete" {
			if deleteConfirmEntry.Text != from {
				dialog.ShowInformation("Confirm the path", "Type the exact relative path (\""+from+"\") into the confirm field to preview the delete.", s.win)
				return
			}
			showManageFilesPreview(s, manageFilesRequest{op: manageOpDelete, locs: locs, from: from})
			return
		}

		to := strings.Trim(strings.TrimSpace(toEntry.Text), "/")
		if to == "" || to == "." {
			dialog.ShowInformation("Missing destination", "Pick or type a \"To\" path first.", s.win)
			return
		}
		showManageFilesPreview(s, manageFilesRequest{op: manageOpMove, locs: locs, from: from, to: to})
	})
	previewBtn.Importance = widget.HighImportance

	optionsCol := container.NewVBox(
		widget.NewLabel("Locations to apply this operation to:"),
		locGroup.CanvasObject(),
		mirrorWarning,
		widget.NewSeparator(),
		opGroup,
		fromForm,
		fromPathError,
		toForm,
		deleteForm,
		container.NewHBox(previewBtn, backBtn),
	)

	expCol := container.NewBorder(
		container.NewVBox(pickerHeader, container.NewHBox(upBtn, breadcrumb)),
		nil, nil, nil,
		list,
	)

	columns := container.NewHSplit(optionsCol, expCol)
	columns.SetOffset(0.35)

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Manage Files", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
		),
		nil, nil, nil,
		columns,
	)
	s.setContent(container.NewPadded(content))
	upBtn.Disable()
	// Restore a persisted Location selection's picker/warning state -
	// locGroup.OnChanged only fires on a user click, not on the initial
	// selection newToggleGroup was constructed with.
	if len(locGroup.Selected()) > 0 {
		updateMirrorWarning()
		loadChildren()
	}
}

// runManageFilesRetime is the Retime operation (see manageOpRetime): it
// recursively lists from at the first selected Location (via
// syncengine.ListRecursive, so this works the same whether that Location is
// local or remote), groups the results into candidate recorder directories
// (recorder.GroupTimestampFiles), then hands them to the shared retime
// pathway (buildTimestampReviewRows) that computes the session consensus and
// each recorder's check exactly as Sync Recorders does, and - if anything
// looks suspicious - shows the same review screen (showTimestampReview)
// before applying anything. The only Retime-specific part is the apply step:
// the correction, once confirmed, is applied at every selected Location
// (local or remote alike, via syncengine.ApplyRenames) - mirroring how a
// recorder's fix already lands at every one of its destDirs in Sync
// Recorders.
func runManageFilesRetime(s *state, locs []syncengine.Location, from string) {
	ctx := context.Background()
	entries, err := syncengine.ListRecursive(ctx, locs[0], from)
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}
	relPaths := make([]string, len(entries))
	for i, e := range entries {
		relPaths[i] = e.RelPath
	}
	groups := recorder.GroupTimestampFiles(relPaths)

	type eligibleGroup struct {
		group recorder.TimestampGroup
		start time.Time
	}
	var eligible []eligibleGroup
	for _, g := range groups {
		var start time.Time
		found := false
		for _, f := range g.Files {
			if t, ok := g.Parser.ParseTimestamp(f.DestRelPath); ok && (!found || t.Before(start)) {
				start = t
				found = true
			}
		}
		if found {
			eligible = append(eligible, eligibleGroup{g, start})
		}
	}
	if len(eligible) == 0 {
		dialog.ShowInformation("Nothing to check",
			"No recorder directories with a checkable timestamp naming pattern were found under "+from+".", s.win)
		return
	}

	tolerance := time.Duration(s.cfg.RecorderSettings.TimestampToleranceMinutes) * time.Minute

	inputs := make([]timestampReviewInput, 0, len(eligible))
	for _, e := range eligible {
		group := e.group
		inputs = append(inputs, timestampReviewInput{
			recorderID:  group.RecorderID,
			parser:      group.Parser,
			sourceFiles: group.Files,
			start:       e.start,
			// Manage Files renames across whichever Locations the user picked,
			// via rclone (local or remote), rather than Sync Recorders' local
			// os.Rename - the one part of the retime that isn't shared.
			apply: func(correct func(time.Time) time.Time) error {
				renames := make(map[string]string, len(group.Files))
				for _, f := range group.Files {
					t, ok := group.Parser.ParseTimestamp(f.DestRelPath)
					if !ok {
						continue
					}
					if newName := group.Parser.RenameForTimestamp(f.DestRelPath, correct(t)); newName != f.DestRelPath {
						renames[f.DestRelPath] = newName
					}
				}
				if len(renames) == 0 {
					return nil
				}
				var firstErr error
				for _, loc := range locs {
					if err := syncengine.ApplyRenames(context.Background(), loc, group.RelDir, renames); err != nil && firstErr == nil {
						firstErr = err
					}
				}
				return firstErr
			},
		})
	}

	reviewRows := buildTimestampReviewRows(inputs, tolerance)
	if len(reviewRows) == 0 {
		dialog.ShowInformation("Nothing to check",
			"No recorder directories with a checkable timestamp naming pattern were found under "+from+".", s.win)
		return
	}

	showTimestampReview(timestampReviewHost{
		s:             s,
		win:           s.win,
		continueLabel: "Apply",
		onContinue:    func() { showManageFiles(s) },
		exitLabel:     "Cancel",
		exitWarning:   "Cancelling now will not apply any timestamp corrections - every recorder's files keep their original names.",
		onExit:        func() { showManageFiles(s) },
	}, reviewRows, tolerance)
}

type manageFilesOp int

const (
	manageOpMove manageFilesOp = iota
	manageOpDelete
)

// manageFilesRequest is the fully-specified operation handed from the setup
// screen (showManageFiles) to the forced preview screen
// (showManageFilesPreview).
type manageFilesRequest struct {
	op   manageFilesOp
	locs []syncengine.Location
	from string
	to   string // only used for manageOpMove
}

// manageFilesLocPlan is one Location's computed plan: the raw
// syncengine plan (needed verbatim by ApplyMove/ApplyDelete) plus the
// per-collision resolution widgets the user picks in the preview screen.
type manageFilesLocPlan struct {
	loc       syncengine.Location
	move      *syncengine.MovePlan
	del       *syncengine.DeletePlan
	err       error
	collision map[string]*widget.Select
}

func (lp *manageFilesLocPlan) fileCount(op manageFilesOp) int {
	switch op {
	case manageOpMove:
		if lp.move == nil {
			return 0
		}
		return len(lp.move.Moves)
	default:
		if lp.del == nil {
			return 0
		}
		return len(lp.del.Entries)
	}
}

func (lp *manageFilesLocPlan) totalBytes(op manageFilesOp) int64 {
	var n int64
	switch op {
	case manageOpMove:
		if lp.move == nil {
			return 0
		}
		for _, m := range lp.move.Moves {
			n += m.Size
		}
	default:
		if lp.del == nil {
			return 0
		}
		for _, e := range lp.del.Entries {
			n += e.Size
		}
	}
	return n
}

// manageFolderKey identifies one row in the Folders column: a directory,
// and (for a move) which side of the operation it's on. A rename/move/
// merge shows every affected directory twice - once as a FROM (source,
// gray/struck-through) entry and once as a TO (destination, blue) entry -
// so renaming a parent folder surfaces both the old and new path for each
// of its children, not just one blended row.
type manageFolderKey struct {
	side string // "from", "to", or "delete"
	dir  string // raw, unprefixed directory
}

// folderRows aggregates this Location's affected files by containing
// directory and side (see manageFolderKey), in first-seen order, for the
// Folders column. Row labels carry a display-only FROM:/TO:/DELETE: prefix;
// keys (parallel to rows) carry the raw form fileRows needs to match files.
func (lp *manageFilesLocPlan) folderRows(op manageFilesOp) (rows []barRow, keys []manageFolderKey) {
	type agg struct {
		count int
		bytes int64
	}
	var order []manageFolderKey
	aggs := map[manageFolderKey]*agg{}
	add := func(key manageFolderKey, size int64) {
		a, ok := aggs[key]
		if !ok {
			a = &agg{}
			aggs[key] = a
			order = append(order, key)
		}
		a.count++
		a.bytes += size
	}
	switch op {
	case manageOpMove:
		if lp.move == nil {
			return nil, nil
		}
		// Two passes (all FROM dirs, then all TO dirs) rather than
		// interleaving per-move, so the Folders column groups its FROM rows
		// together and its TO rows together instead of alternating.
		for _, m := range lp.move.Moves {
			add(manageFolderKey{side: "from", dir: path.Dir(m.SrcRelPath)}, m.Size)
		}
		for _, m := range lp.move.Moves {
			add(manageFolderKey{side: "to", dir: path.Dir(m.DstRelPath)}, m.Size)
		}
	default:
		if lp.del == nil {
			return nil, nil
		}
		for _, e := range lp.del.Entries {
			add(manageFolderKey{side: "delete", dir: path.Dir(e.RelPath)}, e.Size)
		}
	}
	rows = make([]barRow, 0, len(order))
	for i, key := range order {
		a := aggs[key]
		dir := key.dir
		if key.side == "from" {
			dir = strikethroughText(dir)
		}
		rows = append(rows, barRow{
			label:    manageSidePrefix(key.side) + dir,
			summary:  fmt.Sprintf("%d file(s) · %s", a.count, humanBytes(a.bytes)),
			isFolder: true,
			refIdx:   i,
		})
	}
	return rows, order
}

// fileRows lists this Location's affected files matching key (a specific
// Folders-column row: one directory, one side), for the Files column.
func (lp *manageFilesLocPlan) fileRows(op manageFilesOp, key manageFolderKey) []barRow {
	var rows []barRow
	switch op {
	case manageOpMove:
		if lp.move == nil {
			return nil
		}
		for _, m := range lp.move.Moves {
			switch key.side {
			case "from":
				if path.Dir(m.SrcRelPath) != key.dir {
					continue
				}
				rows = append(rows, barRow{
					label:   "FROM: " + strikethroughText(path.Base(m.SrcRelPath)),
					summary: humanBytes(m.Size),
				})
			case "to":
				if path.Dir(m.DstRelPath) != key.dir {
					continue
				}
				rows = append(rows, barRow{
					label:   "TO: " + path.Base(m.DstRelPath),
					summary: humanBytes(m.Size),
				})
			}
		}
	default:
		if lp.del == nil {
			return nil
		}
		for _, e := range lp.del.Entries {
			if path.Dir(e.RelPath) != key.dir {
				continue
			}
			rows = append(rows, barRow{label: "DELETE: " + path.Base(e.RelPath), summary: humanBytes(e.Size)})
		}
	}
	return rows
}

// manageSidePrefix is the display-only label prefix for a manageFolderKey
// side.
func manageSidePrefix(side string) string {
	switch side {
	case "from":
		return "FROM: "
	case "to":
		return "TO: "
	default:
		return "DELETE: "
	}
}

// manageBarList builds a chip list (the same visual row primitive as the
// sync/scan progress screen - see createBackingBarItem/updateBackingBarItem
// in progress_widgets.go) backed by *rows. tintFor, if non-nil, is called
// per row to wash its background that color regardless of progress (0
// here - these rows never fill); isSelected (may be nil) drives the
// selection outline.
func manageBarList(win fyne.Window, rows *[]barRow, tintFor func(barRow) color.Color, isSelected func(barRow) bool) *widget.List {
	return widget.NewList(
		func() int { return len(*rows) },
		func() fyne.CanvasObject { return createBackingBarItem(win) },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if int(id) < 0 || int(id) >= len(*rows) {
				return
			}
			row := (*rows)[id]
			sel := isSelected != nil && isSelected(row)
			updateBackingBarItem(obj, row.label, row.summary, 0, nil, false, row.isFolder, sel, win, "")
			if tintFor != nil {
				if c := tintFor(row); c != nil {
					tintItemBg(obj, c)
				}
			}
		},
	)
}

// manageRowTint picks a Folders/Files row's background wash from its
// display-only side prefix (see manageSidePrefix): gray+struck-through for
// a FROM (source, going away) row, blue for a TO (destination) row, red for
// a DELETE row.
func manageRowTint(row barRow) color.Color {
	switch {
	case strings.HasPrefix(row.label, "FROM: "):
		return manageColorFromBg
	case strings.HasPrefix(row.label, "TO: "):
		return manageColorMoveBg
	case strings.HasPrefix(row.label, "DELETE: "):
		return manageColorDeleteBg
	default:
		return nil
	}
}

// showManageFilesPreview is the mandatory second screen for every Manage
// Files operation: it computes (via syncengine.PlanMove/PlanDelete) and
// displays the exact final-state effect of req at every selected Location
// before Apply is reachable at all. It reuses the sync/scan progress
// screen's three-column chip layout (Locations/Folders/Files, all
// navigable, each chip showing a file count and size) rather than a flat
// list. A move/rename/merge shows every affected path twice: a gray,
// struck-through FROM row for where it is now, and a blue TO row for where
// it's going (see manageFolderKey) - so renaming a parent folder surfaces
// both the old and new path for each child. A delete shows every affected
// path once, tinted red.
func showManageFilesPreview(s *state, req manageFilesRequest) {
	// previewTitle and applyingTitle both name the exact operation - the
	// only difference is the verb - so the header reads the same way
	// before and during Apply, just swapping "Preview" for "Applying".
	previewTitle := "Preview: " + req.from + " → " + req.to
	applyingTitle := "Applying: " + req.from + " → " + req.to
	verb := "moved"
	if req.op == manageOpDelete {
		previewTitle = "Preview: DELETE " + req.from
		applyingTitle = "Applying: DELETE " + req.from
		verb = "permanently deleted"
	}
	titleLabel := widget.NewLabelWithStyle(previewTitle, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	locsValue := widget.NewLabelWithStyle("0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	filesValue := widget.NewLabelWithStyle("0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	bytesValue := widget.NewLabelWithStyle("0 B", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	locsBlurb := metricPanel("Locations", locsValue, color.NRGBA{R: 232, G: 240, B: 254, A: 255})
	filesBlurb := metricPanel("Files "+verb, filesValue, color.NRGBA{R: 255, G: 239, B: 219, A: 255})
	bytesBlurb := metricPanel("Bytes", bytesValue, color.NRGBA{R: 243, G: 232, B: 255, A: 255})
	metrics := container.NewGridWithColumns(3, locsBlurb, filesBlurb, bytesBlurb)

	errorLabel := widget.NewLabel("")
	errorLabel.Wrapping = fyne.TextWrapWord
	errorLabel.Hide()

	collisionsBtn := widget.NewButton("", nil)
	collisionsBtn.Importance = widget.WarningImportance
	collisionsBtn.Hide()

	// computeLoading runs while PlanMove/PlanDelete (+ size lookups) are
	// being computed; applyLoading runs while Apply is actually moving/
	// deleting files. Same thin infinite bar destFolderBrowser and Sync
	// Experiments' scan use (see newLoadingBar in progress_widgets.go).
	computeLoading := newLoadingBar()
	applyLoading := newLoadingBar()

	var plans []*manageFilesLocPlan
	var locRows, foldRows, fileRows []barRow
	// foldKeys is parallel to foldRows: the raw (unprefixed) directory+side
	// each row represents, since foldRows[i].label carries a display-only
	// FROM:/TO:/DELETE: prefix that fileRows' own path.Dir matching must not
	// see.
	var foldKeys []manageFolderKey
	selectedLoc, selectedFoldIdx := -1, -1

	locList := manageBarList(s.win, &locRows, nil, func(r barRow) bool { return r.refIdx == selectedLoc })
	foldList := manageBarList(s.win, &foldRows, manageRowTint, func(r barRow) bool { return r.refIdx == selectedFoldIdx })
	fileList := manageBarList(s.win, &fileRows, manageRowTint, nil)

	refreshFiles := func() {
		fileRows = nil
		if selectedLoc >= 0 && selectedLoc < len(plans) && selectedFoldIdx >= 0 && selectedFoldIdx < len(foldKeys) {
			fileRows = plans[selectedLoc].fileRows(req.op, foldKeys[selectedFoldIdx])
		}
		fileList.Refresh()
	}
	refreshFolders := func() {
		foldRows, foldKeys = nil, nil
		if selectedLoc >= 0 && selectedLoc < len(plans) {
			foldRows, foldKeys = plans[selectedLoc].folderRows(req.op)
		}
		selectedFoldIdx = -1
		if len(foldRows) > 0 {
			selectedFoldIdx = 0
		}
		foldList.Refresh()
		refreshFiles()
	}
	selectLoc := func(idx int) {
		selectedLoc = idx
		locList.Refresh()
		refreshFolders()
	}

	locList.OnSelected = func(id widget.ListItemID) {
		if int(id) < 0 || int(id) >= len(locRows) {
			return
		}
		selectLoc(locRows[id].refIdx)
	}
	foldList.OnSelected = func(id widget.ListItemID) {
		if int(id) < 0 || int(id) >= len(foldRows) {
			return
		}
		selectedFoldIdx = foldRows[id].refIdx
		foldList.Refresh()
		refreshFiles()
	}

	backBtn := widget.NewButton("Back", func() { showManageFiles(s) })
	applyBtn := widget.NewButton("Apply", nil)
	applyBtn.Importance = widget.DangerImportance
	applyBtn.Disable()

	// managePlanCompute is the pure-data result of planning one Location's
	// move/delete - computed off the UI thread. Widgets (the per-collision
	// Select) are only ever created back on the UI thread, from this, once
	// the goroutine below hands it to fyne.Do.
	type managePlanCompute struct {
		loc  syncengine.Location
		move *syncengine.MovePlan
		del  *syncengine.DeletePlan
		err  error
	}

	buildPlans := func() {
		computeLoading.Show()
		backBtn.Disable()
		applyBtn.Disable()
		collisionsBtn.Hide()
		errorLabel.Hide()
		plans, locRows, foldRows, foldKeys, fileRows = nil, nil, nil, nil, nil
		selectedLoc, selectedFoldIdx = -1, -1
		locList.Refresh()
		foldList.Refresh()
		fileList.Refresh()

		locs, op, from, to := req.locs, req.op, req.from, req.to
		go func() {
			ctx := context.Background()
			results := make([]managePlanCompute, len(locs))
			// Every Location is independent - plan them all concurrently
			// rather than paying each one's recursive-listing round trips
			// back to back.
			var wg sync.WaitGroup
			for i, loc := range locs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					c := managePlanCompute{loc: loc}
					switch op {
					case manageOpMove:
						plan, err := syncengine.PlanMove(ctx, loc, from, to)
						if err != nil {
							c.err = err
							break
						}
						c.move = &plan
					case manageOpDelete:
						plan, err := syncengine.PlanDelete(ctx, loc, from)
						if err != nil {
							c.err = err
							break
						}
						c.del = &plan
					}
					results[i] = c
				}()
			}
			wg.Wait()

			fyne.Do(func() {
				var totalFiles, uniqueFiles int
				var totalBytes int64
				var errs []string
				var collisionCount int

				for _, c := range results {
					lp := &manageFilesLocPlan{loc: c.loc, move: c.move, del: c.del, err: c.err, collision: map[string]*widget.Select{}}
					if lp.err != nil {
						errs = append(errs, lp.loc.Name+": "+lp.err.Error())
					} else if lp.move != nil {
						for _, cPath := range lp.move.Collisions {
							sel := widget.NewSelect([]string{"Skip", "Overwrite", "Keep both"}, nil)
							sel.SetSelected("Skip")
							lp.collision[cPath] = sel
							collisionCount++
						}
					}
					plans = append(plans, lp)
					totalFiles += lp.fileCount(req.op)
					totalBytes += lp.totalBytes(req.op)
					// uniqueFiles is the largest single-Location file count:
					// mirrored Locations should hold the same set of relative
					// paths, so this is "how many distinct files" as opposed
					// to totalFiles, which counts the same file once per
					// Location it's applied to.
					if lp.err == nil && lp.fileCount(req.op) > uniqueFiles {
						uniqueFiles = lp.fileCount(req.op)
					}
				}

				locRows = make([]barRow, len(plans))
				for i, lp := range plans {
					summary := fmt.Sprintf("%d file(s) · %s", lp.fileCount(req.op), humanBytes(lp.totalBytes(req.op)))
					locRows[i] = barRow{label: lp.loc.Name, summary: summary, err: lp.err, hasError: lp.err != nil, refIdx: i}
				}
				locList.Refresh()

				locsValue.SetText(fmt.Sprint(len(plans)))
				filesValue.SetText(fmt.Sprintf("%d unique/%d total", uniqueFiles, totalFiles))
				bytesValue.SetText(humanBytes(totalBytes))

				if len(errs) > 0 {
					errorLabel.SetText("Some Locations could not be previewed:\n" + strings.Join(errs, "\n"))
					errorLabel.Show()
				} else {
					errorLabel.Hide()
				}

				if collisionCount > 0 {
					collisionsBtn.SetText(fmt.Sprintf("Resolve %d collision(s)…", collisionCount))
					collisionsBtn.Show()
				} else {
					collisionsBtn.Hide()
				}

				computeLoading.Hide()
				backBtn.Enable()
				if len(plans) > len(errs) {
					applyBtn.Enable()
				} else {
					applyBtn.Disable()
				}

				if len(plans) > 0 {
					selectLoc(0)
					return
				}
				foldRows, foldKeys, fileRows = nil, nil, nil
				selectedFoldIdx = -1
				foldList.Refresh()
				fileList.Refresh()
			})
		}()
	}
	buildPlans()

	collisionsBtn.OnTapped = func() { showManageCollisionsDialog(s.win, plans, req.from, req.to) }

	foldFilesSplit := container.NewHSplit(
		createColumn("Folders", foldList),
		createColumn("Files", fileList),
	)
	foldFilesSplit.SetOffset(0.5)
	columns := container.NewHSplit(
		createColumn("Locations", locList),
		foldFilesSplit,
	)
	columns.SetOffset(1.0 / 3.0)

	// applyTask is one Location's fully-resolved apply work: the raw
	// syncengine calls plus (for a move) resolutions read off the
	// collision Selects while still on the UI thread, so the goroutine
	// below never touches a widget.
	type applyTask struct {
		loc         syncengine.Location
		move        *syncengine.MovePlan
		resolutions map[string]syncengine.CollisionResolution
	}

	// buttonRow holds Back/Apply until the operation finishes, then gets
	// swapped for a single Done button (see applyBtn.OnTapped below).
	buttonRow := container.NewHBox(backBtn, applyBtn)

	applyBtn.OnTapped = func() {
		run := func() {
			var tasks []applyTask
			for _, lp := range plans {
				if lp.err != nil {
					continue
				}
				if req.op == manageOpMove && lp.move == nil {
					continue
				}
				t := applyTask{loc: lp.loc, move: lp.move}
				if req.op == manageOpMove {
					t.resolutions = make(map[string]syncengine.CollisionResolution, len(lp.collision))
					for p, sel := range lp.collision {
						switch sel.Selected {
						case "Overwrite":
							t.resolutions[p] = syncengine.CollisionOverwrite
						case "Keep both":
							t.resolutions[p] = syncengine.CollisionKeepBoth
						default:
							t.resolutions[p] = syncengine.CollisionSkip
						}
					}
				}
				tasks = append(tasks, t)
			}

			titleLabel.SetText(applyingTitle)
			applyLoading.Show()
			backBtn.Disable()
			applyBtn.Disable()
			go func() {
				var failed []string
				ctx := context.Background()
				for _, t := range tasks {
					var err error
					switch req.op {
					case manageOpMove:
						err = syncengine.ApplyMove(ctx, t.loc, *t.move, t.resolutions)
					case manageOpDelete:
						err = syncengine.ApplyDelete(ctx, t.loc, req.from)
					}
					if err != nil {
						failed = append(failed, t.loc.Name+": "+err.Error())
					}
				}
				fyne.Do(func() {
					applyLoading.Hide()
					if len(failed) > 0 {
						titleLabel.SetText(previewTitle)
						backBtn.Enable()
						applyBtn.Enable()
						dialog.ShowError(fmt.Errorf("some Locations failed - mirrors may now differ:\n%s", strings.Join(failed, "\n")), s.win)
						return
					}
					// Stay on this screen with the final counts/columns still
					// visible rather than force-closing into a modal - just
					// swap the title and the footer's Back/Apply for a
					// single Done that returns to the setup screen.
					titleLabel.SetText("Operation complete!")
					doneBtn := widget.NewButton("Done", func() { showManageFiles(s) })
					doneBtn.Importance = widget.HighImportance
					buttonRow.Objects = []fyne.CanvasObject{doneBtn}
					buttonRow.Refresh()
				})
			}()
		}

		if req.op == manageOpDelete {
			var fileCount, locCount int
			for _, lp := range plans {
				if lp.err != nil {
					continue
				}
				locCount++
				if n := lp.fileCount(req.op); n > fileCount {
					fileCount = n
				}
			}
			message := fmt.Sprintf("This will permanently delete %s selected file(s) across the %d location(s). This cannot be undone.",
				commaInt(fileCount), locCount)
			showIrreversibleDeleteConfirm(s, message, nil, "Delete Permanently", run)
			return
		}
		run()
	}

	content := container.NewBorder(
		container.NewVBox(
			titleLabel,
			computeLoading.CanvasObject(),
			metrics,
			errorLabel,
			collisionsBtn,
			widget.NewSeparator(),
		),
		container.NewVBox(applyLoading.CanvasObject(), buttonRow),
		nil, nil,
		columns,
	)
	s.setContent(container.NewPadded(content))
}

// showManageCollisionsDialog lists every destination path a move/merge
// would collide with, across all previewed Locations, each with the same
// Skip/Overwrite/Keep-both selector shown in the Locations' plans - the
// live *widget.Select values buildPlans stored on each manageFilesLocPlan,
// so editing a choice here is exactly what Apply reads.
func showManageCollisionsDialog(win fyne.Window, plans []*manageFilesLocPlan, from, to string) {
	box := container.NewVBox(widget.NewLabel(fmt.Sprintf("Moving %q to %q collides with existing files below. Choose how to resolve each.", from, to)))
	for _, lp := range plans {
		if len(lp.collision) == 0 {
			continue
		}
		box.Add(widget.NewSeparator())
		box.Add(widget.NewLabelWithStyle(lp.loc.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		for _, c := range lp.move.Collisions {
			sel, ok := lp.collision[c]
			if !ok {
				continue
			}
			box.Add(container.NewBorder(nil, nil, widget.NewLabel(c), nil, sel))
		}
	}
	scroll := container.NewVScroll(box)
	scroll.SetMinSize(fyne.NewSize(480, 320))

	var d dialog.Dialog
	closeBtn := widget.NewButton("Done", func() { d.Hide() })
	content := container.NewBorder(nil, container.NewCenter(closeBtn), nil, nil, scroll)
	d = dialog.NewCustomWithoutButtons("Resolve Collisions", content, win)
	d.Show()
}
