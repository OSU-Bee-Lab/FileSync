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

// manageOpRetime is the radio label for the "Retime" option - checking and
// correcting recorder clock errors already synced to disk, via the same
// recorder.CheckRecorderTimestamp/ApplyTimestampFix
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
	// Audio and Results Locations are offered together, grouped by role: an
	// operation typed in audio terms applies to both trees at once, with
	// each Results Location's counterpart file resolved from the audio name
	// (see syncengine.resolveResultsLeaf).
	locGroup := newRoleToggleGroup(s.cfg.Locations, selectedFromIDs(s.cfg.Locations, s.cfg.ManageFilesLocationIDs))
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

	// --- shared path picker: two destFolderBrowser instances (one per
	// From/To target), swapped in a single slot. This routes Manage Files
	// through the same browser widget the other four browse call sites use,
	// instead of a bespoke list/breadcrumb/add-row/highlight reimplementation.
	// Each browser mirrors its chosen path into its own From/To entry via
	// OnPathChanged; Delete only ever targets "From". ---
	deleteConfirmEntry := widget.NewEntry()
	deleteConfirmEntry.SetPlaceHolder("type the exact relative path to confirm")
	deleteConfirmEntry.SetText(s.manageFilesDeleteConfirm)
	deleteConfirmEntry.OnChanged = func(t string) { s.manageFilesDeleteConfirm = t }

	pickerTarget := "From" // "From" or "To" - which target the picker currently populates
	if s.manageFilesPickerTarget == "To" {
		pickerTarget = "To"
	}
	// pickerHeaderLabel is the browser's own banner (not a separate row
	// beneath it), naming the field the visible browser populates.
	pickerHeaderBg := canvas.NewRectangle(color.NRGBA{R: 240, G: 242, B: 245, A: 255})
	pickerHeaderLabel := widget.NewLabelWithStyle("From", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	pickerHeader := container.NewStack(pickerHeaderBg, container.NewPadded(pickerHeaderLabel))

	selectedLocs := func() []syncengine.Location {
		return locationsFromNamesAny(s.cfg.Locations, locGroup.Selected())
	}

	var fromEntry, toEntry *widget.Entry
	targetEntry := func() *widget.Entry {
		if pickerTarget == "To" {
			return toEntry
		}
		return fromEntry
	}

	// newManageBrowser builds one From/To browser. Both list the union of
	// every currently selected Location one level at a time via the shared
	// browser's custom-lister hook - a folder only has to exist on one
	// selected Location to show up (an operation just skips over the
	// Locations that don't hold it), so Manage Files can't use plain
	// syncengine.ListChildren against a single reference Location the way it
	// used to. It still needs syncengine.ListChildrenUnion's not-found
	// tolerance (naming a new "To" folder, or selecting a bare file) plus its
	// error routing (every selected Location's token expired → reconnect).
	// Only "To" (allowCreate) offers the "+ Add Folder" row.
	newManageBrowser := func(allowCreate bool) *destFolderBrowser {
		b := newDestFolderBrowser(s.win, allowCreate)
		b.showFiles = true
		b.selectFiles = true
		b.showFileSize = true
		b.breadcrumbPrefix = "experiments/"
		b.addFolderStatus = "Folder will be created on Apply."
		b.lister = func(gen int, relPath string) {
			locs := selectedLocs()
			if len(locs) == 0 {
				b.listingDone(gen, nil, nil)
				b.setBreadcrumbOverride("Select a Location above first.")
				return
			}
			go func() {
				result, notFound, isFile, pres, err := syncengine.ListChildrenUnion(context.Background(), locs, relPath)
				fyne.Do(func() {
					if err != nil {
						// Every selected Location failed with something other
						// than not-found/is-a-file (e.g. an expired remote
						// token) - route to reconnect via showLocationError.
						if b.listingFailed(gen) {
							showLocationError(s, err, locs...)
						}
						return
					}
					if notFound || isFile {
						// A folder that doesn't exist on any selected Location
						// yet (naming a new "To" destination) or that names a
						// bare file (which has no children) is expected, not
						// an error - show it empty.
						if b.listingDone(gen, nil, nil) && b.allowCreate && notFound {
							b.setBreadcrumbNote(" (new folder)")
						}
						return
					}
					b.listingDone(gen, result, pres)
				})
			}()
		}
		return b
	}
	browserFrom := newManageBrowser(false)
	browserTo := newManageBrowser(true)
	activeBrowser := func() *destFolderBrowser {
		if pickerTarget == "To" {
			return browserTo
		}
		return browserFrom
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

	// setPickerTarget swaps which browser (and which underlying From/To
	// field) is active, re-anchoring the now-visible browser at that field's
	// current text so typing and browsing stay in sync.
	var setPickerTarget func(target string)
	setPickerTarget = func(target string) {
		pickerTarget = target
		s.manageFilesPickerTarget = target
		pickerHeaderLabel.SetText(target)
		if target == "To" {
			browserFrom.CanvasObject().Hide()
			browserTo.CanvasObject().Show()
		} else {
			browserTo.CanvasObject().Hide()
			browserFrom.CanvasObject().Show()
		}
		activeBrowser().NavigateTo(strings.Trim(strings.TrimSpace(targetEntry().Text), "/"))
	}

	fromFocusEntry := newFocusEntry(func() { setPickerTarget("From") }, func() { validateFromPath() })
	fromFocusEntry.SetPlaceHolder("experiments/<relative path to rename/move/delete>")
	toFocusEntry := newFocusEntry(func() { setPickerTarget("To") }, nil)
	toFocusEntry.SetPlaceHolder("experiments/<new name or destination folder>")
	fromEntry = &fromFocusEntry.Entry
	toEntry = &toFocusEntry.Entry
	fromEntry.OnChanged = func(t string) { s.manageFilesFrom = t }
	toEntry.OnChanged = func(t string) { s.manageFilesTo = t }
	if s.manageFilesFrom != "" {
		fromEntry.SetText(s.manageFilesFrom)
	}
	if s.manageFilesTo != "" {
		toEntry.SetText(s.manageFilesTo)
	}

	// Each browser mirrors its chosen path (a browsed folder, or a tapped
	// file) into its own From/To field; "From" also re-validates on change.
	browserFrom.OnPathChanged = func(rel string) {
		fromEntry.SetText(rel)
		validateFromPath()
	}
	browserTo.OnPathChanged = func(rel string) {
		toEntry.SetText(rel)
	}

	// Submitting a typed path drives the active browser to it (a not-found /
	// is-a-file path is tolerated by the lister above).
	fromEntry.OnSubmitted = func(text string) {
		if pickerTarget == "From" {
			browserFrom.NavigateTo(text)
		}
	}
	toEntry.OnSubmitted = func(text string) {
		if pickerTarget == "To" {
			browserTo.NavigateTo(text)
		}
	}

	// setLocs points both browsers at the currently selected Locations (for
	// the add-folder row's enabled state) and re-lists whichever is active.
	setLocs := func() {
		locs := selectedLocs()
		browserFrom.SetBrowseLocations(locs)
		browserTo.SetBrowseLocations(locs)
		activeBrowser().reload()
	}

	locGroup.OnChanged = func(sel []string) {
		s.cfg.ManageFilesLocationIDs = idsFromLocations(locationsFromNamesAny(s.cfg.Locations, sel))
		s.saveConfig()
		updateMirrorWarning()
		// Changing which Locations the operation applies to shouldn't disturb
		// the already-typed/browsed From/To paths - just re-list the (possibly
		// new) reference Location at the current path.
		setLocs()
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
		s.manageFilesOp = v
		toForm.Hide()
		deleteForm.Hide()
		switch v {
		case "Rename / Move / Merge":
			toForm.Show()
		case "Delete":
			deleteForm.Show()
		}
	}
	initialOp := s.manageFilesOp
	if initialOp == "" {
		initialOp = "Rename / Move / Merge"
	}
	opGroup.SetSelected(initialOp)

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
		actionRow(backBtn, previewBtn),
	)

	// browserSlot stacks both browsers; only the active target's is shown
	// (see setPickerTarget).
	browserSlot := container.NewStack(browserFrom.CanvasObject(), browserTo.CanvasObject())
	expCol := container.NewBorder(pickerHeader, nil, nil, nil, browserSlot)

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
	// Show the previously-active browser (From, or a restored To) first,
	// then point both browsers at the current selection and list it
	// (restoring a persisted Location's picker/warning state -
	// locGroup.OnChanged only fires on a user click, not the initial
	// selection newToggleGroup was constructed with).
	setPickerTarget(pickerTarget)
	if len(locGroup.Selected()) > 0 {
		updateMirrorWarning()
	}
	setLocs()
}

// runManageFilesRetime is the Retime operation (see manageOpRetime): it
// recursively lists from at the first selected Location (via
// syncengine.ListRecursive, so this works the same whether that Location is
// local or remote), groups the results into candidate recorder directories
// (recorder.GroupTimestampFiles), then hands them to the shared retime
// pathway (buildTimestampReviewRows) that computes the session consensus and
// each recorder's check exactly as Sync Recorders does, and always shows the
// same review screen (showTimestampReview) - even when every recorder checks
// out clean, so the researcher gets an explicit all-clear rather than no
// feedback at all - before applying anything. The only Retime-specific part
// is the apply step:
// the correction, once confirmed, is applied at every selected Location
// (local or remote alike, via syncengine.ApplyRenames) - mirroring how a
// recorder's fix already lands at every one of its destDirs in Sync
// Recorders.
func runManageFilesRetime(s *state, locs []syncengine.Location, from string) {
	ctx := context.Background()
	// The listing has to come from an Audio Location: every recorder's
	// timestamp parser reads and rebuilds that recorder's own audio
	// filenames (see recorder.TimestampParser), which a Results Location's
	// "<stem>_buzzdetect.csv" names aren't. The corrections still land on
	// every selected Location - syncengine.ApplyRenames maps each rename to
	// the counterpart file at a Results Location.
	audio := filterByRole(locs, syncengine.RoleAudio)
	if len(audio) == 0 {
		dialog.ShowInformation("Select an Audio location",
			"Retime reads recorder timestamps from audio filenames, so at least one Audio location must be selected. Any Results locations selected alongside it have their matching result files renamed too.", s.win)
		return
	}
	entries, err := syncengine.ListRecursive(ctx, audio[0], from)
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
			locs:        locs,
			relDir:      group.RelDir,
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

	showTimestampReview(timestampReviewHost{
		s:             s,
		win:           s.win,
		parentPath:    locs[0].Name + ": " + from,
		continueLabel: "Apply Corrections",
		onContinue:    func() { showManageFiles(s) },
		exitLabel:     "Back Without Applying",
		exitWarning:   "Going back now will not apply any timestamp corrections - every recorder's files keep their original names.",
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
			summary:  fmt.Sprintf("%s · %s", plural(a.count, "file", ""), humanBytes(a.bytes)),
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
	// Red is reserved for the delete, which really does destroy data; a
	// move/merge is the ordinary affirmative action of this screen and is
	// styled like every other screen's - and both say which operation they're
	// about to run rather than a bare "Apply", since this screen is reached
	// for either. The ellipsis on Delete marks the irreversible-action confirm
	// that follows.
	applyBtn := widget.NewButton("Apply Move", nil)
	applyBtn.Importance = widget.HighImportance
	if req.op == manageOpDelete {
		applyBtn.SetText("Delete Files…")
		applyBtn.Importance = widget.DangerImportance
	}
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
					summary := fmt.Sprintf("%s · %s", plural(lp.fileCount(req.op), "file", ""), humanBytes(lp.totalBytes(req.op)))
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
					collisionsBtn.SetText(fmt.Sprintf("Resolve %s…", plural(collisionCount, "collision", "")))
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
	buttonRow := actionRow(backBtn, applyBtn)

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
				// Locations are independent of one another, so apply them
				// concurrently - otherwise a fast local Location waits behind
				// however long the slow remote one takes.
				errs := make([]string, len(tasks))
				ctx := context.Background()
				var wg sync.WaitGroup
				for i, t := range tasks {
					wg.Add(1)
					go func() {
						defer wg.Done()
						var err error
						switch req.op {
						case manageOpMove:
							err = syncengine.ApplyMove(ctx, t.loc, *t.move, t.resolutions)
						case manageOpDelete:
							err = syncengine.ApplyDelete(ctx, t.loc, req.from)
						}
						if err != nil {
							errs[i] = t.loc.Name + ": " + err.Error()
						}
					}()
				}
				wg.Wait()
				var failed []string
				for _, e := range errs {
					if e != "" {
						failed = append(failed, e)
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
			message := fmt.Sprintf("This will permanently delete %s selected %s across %s. This cannot be undone.",
				commaInt(fileCount), pluralWord(fileCount, "file", ""), plural(locCount, "location", ""))
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
