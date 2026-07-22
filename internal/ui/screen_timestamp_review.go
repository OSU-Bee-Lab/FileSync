package ui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
)

// timestampReviewRow is one recorder's line on the timestamp review screen.
// apply performs the correction (renaming sourceFiles per
// parser.RenameForTimestamp(f, correct(oldTime))) wherever this recorder's
// files actually live - Sync Recorders always renames local destDirs
// directly (recorder.ApplyTimestampFix), while Manage Files' Retime applies
// across whichever Locations (local or remote) the user selected, via
// rclone (syncengine.ApplyRenames) - so this row doesn't need to know or
// care which.
type timestampReviewRow struct {
	recorderID  string
	parser      recorder.TimestampParser
	sourceFiles []recorder.SourceFile
	check       recorder.TimestampIssue
	apply       func(correct func(time.Time) time.Time) error
}

// timestampIssueLabel describes a check's Kind in plain language for the
// review screen.
func timestampIssueLabel(kind recorder.TimestampIssueKind) string {
	switch kind {
	case recorder.IssueWrongYear:
		return "looks off by a year"
	case recorder.IssueWrongMonth:
		return "looks off by a month"
	case recorder.IssueWrongDay:
		return "looks off by a day"
	case recorder.IssueAMPM:
		return "looks like an AM/PM mismatch"
	case recorder.IssueOther:
		return "doesn't match the other recorders - check manually"
	default:
		return "looks correct"
	}
}

// timestampCardColor mirrors the active-sync screen's colored-row language
// (see rowBackgroundColor): orange marks a recorder the detector flagged,
// gray is the neutral/"looks correct" state - deliberately not the same
// transparent "untinted" idle look Screen 2 uses for jobIdle, since here
// every recorder should read as reviewed, not merely pending.
func timestampCardColor(suspicious bool) color.Color {
	if suspicious {
		return colorRGBA(0xE0, 0x7B, 0x4A, 0xFF)
	}
	return colorRGBA(0xDD, 0xDD, 0xDD, 0xFF)
}

// timestampCardColorFor picks a card's background from its live entry state:
// blue whenever the user has opted into a new start time for this recorder
// (overriding the suspicious/neutral coloring, since the user's edit is now
// the more important signal than the detector's original verdict), otherwise
// the usual suspicious/neutral color.
func timestampCardColorFor(e *timestampReviewEntry) color.Color {
	if e.adjust {
		return colorRGBA(0x4A, 0x7B, 0xE0, 0xFF)
	}
	return timestampCardColor(e.row.check.Suspicious)
}

// ordinalDay formats a day-of-month with its English ordinal suffix
// (1st, 2nd, 3rd, 4th, ... 11th-13th, 21st, ...), for plain-language
// timestamp previews.
func ordinalDay(d int) string {
	if d >= 11 && d <= 13 {
		return fmt.Sprintf("%dth", d)
	}
	switch d % 10 {
	case 1:
		return fmt.Sprintf("%dst", d)
	case 2:
		return fmt.Sprintf("%dnd", d)
	case 3:
		return fmt.Sprintf("%drd", d)
	default:
		return fmt.Sprintf("%dth", d)
	}
}

// plainDateTime renders t as e.g. "June 3rd 2026 at 3:45 PM" for the review
// screen's live preview of the edited start time.
func plainDateTime(t time.Time) string {
	return fmt.Sprintf("%s %s %d at %s", t.Month().String(), ordinalDay(t.Day()), t.Year(), t.Format("3:04 PM"))
}

// signedUnit formats n with an explicit sign and a singular/plural unit
// name, e.g. signedUnit(-1, "month") -> "-1 month", signedUnit(12, "hour")
// -> "+12 hours".
func signedUnit(n int, unit string) string {
	sign := "+"
	if n < 0 {
		sign = "-"
		n = -n
	}
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%s%d %s", sign, n, unit)
}

// formatAdjustment describes the edit from -> to as a compound, plain-
// language delta, e.g. "-1 month; +12 hours" - each calendar component
// (year, month, day, hour, minute) is diffed independently rather than
// normalized into a single duration, since a recorder's clock error is
// naturally described that way (e.g. "the month was wrong" rather than "off
// by ~30 days").
func formatAdjustment(from, to time.Time) string {
	var parts []string
	if d := to.Year() - from.Year(); d != 0 {
		parts = append(parts, signedUnit(d, "year"))
	}
	if d := int(to.Month()) - int(from.Month()); d != 0 {
		parts = append(parts, signedUnit(d, "month"))
	}
	if d := to.Day() - from.Day(); d != 0 {
		parts = append(parts, signedUnit(d, "day"))
	}
	if d := to.Hour() - from.Hour(); d != 0 {
		parts = append(parts, signedUnit(d, "hour"))
	}
	if d := to.Minute() - from.Minute(); d != 0 {
		parts = append(parts, signedUnit(d, "minute"))
	}
	if len(parts) == 0 {
		return "no change"
	}
	return strings.Join(parts, "; ")
}

// strikethrough overlays a combining strikethrough mark on every rune of s,
// used to show a file's old (about to be replaced) name in the review
// screen's rename preview. Fyne's canvas/widget text has no native
// strikethrough style, so this is done with Unicode's combining
// long-stroke-overlay character rather than a custom-drawn text renderer.
func strikethrough(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(r)
		b.WriteRune('̶')
	}
	return b.String()
}

// timestampReviewEntry is the live, editable state for one recorder on the
// review screen - separate from timestampReviewRow (the immutable detection
// result) so switching which recorder is selected doesn't lose whatever the
// user already typed or toggled for the others.
type timestampReviewEntry struct {
	row    timestampReviewRow
	adjust bool
	text   string
}

// timestampReviewHost supplies the pieces of a caller's screen that
// showTimestampReview needs but doesn't own: where to draw (s, win), what
// the Continue/Exit actions actually do, and an optional per-recorder hook
// run right after ApplyTimestampFix (Sync Recorders uses this to re-upload a
// corrected file outside batch-upload mode; Manage Files' Retime leaves it
// nil since it has no uploads to redo).
type timestampReviewHost struct {
	s   *state
	win fyne.Window

	continueLabel string
	onContinue    func()

	exitLabel   string
	exitWarning string
	onExit      func()

	afterFix func(row timestampReviewRow, delta time.Duration)
}

// timestampReviewScreen holds the live state for the master-detail
// timestamp review step: a left-hand list of recorders (colored per
// timestampCardColor) and a right-hand detail pane for whichever one is
// selected, showing its files and a live rename preview.
type timestampReviewScreen struct {
	host     timestampReviewHost
	entries  []*timestampReviewEntry
	selected int

	cards      []*canvas.Rectangle
	cardLabels []*widget.Label
	detailBox  *fyne.Container
}

// showTimestampReview shows the full-screen review step between a caller
// (Sync Recorders' end-of-session check, or Manage Files' Retime scan) and
// whatever it does next - see timestampReviewHost. Continuing applies every
// checked recorder's correction - parsed from its entry, generalized to a
// uniform offset from that recorder's own recorded time, and applied to
// every file from it (see recorder.ApplyTimestampFix) - then calls
// host.onContinue.
func showTimestampReview(host timestampReviewHost, rows []timestampReviewRow) {
	sorted := make([]timestampReviewRow, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].check.Suspicious && !sorted[j].check.Suspicious
	})

	tr := &timestampReviewScreen{host: host}
	for _, r := range sorted {
		tr.entries = append(tr.entries, &timestampReviewEntry{
			row:    r,
			adjust: false,
			text:   r.check.Suggested.Format("2006-01-02 15:04"),
		})
	}

	leftBox := container.NewVBox()
	for i, e := range tr.entries {
		i := i
		bg := canvas.NewRectangle(timestampCardColorFor(e))
		idLabel := widget.NewLabelWithStyle(e.row.recorderID, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		statusLabel := widget.NewLabel(timestampIssueLabel(e.row.check.Kind))
		cell := container.NewStack(bg, container.NewPadded(container.NewVBox(idLabel, statusLabel)))
		card := newTappableCard(cell, func() { tr.selectRow(i) })
		leftBox.Add(card)
		tr.cards = append(tr.cards, bg)
		tr.cardLabels = append(tr.cardLabels, statusLabel)
	}

	tr.detailBox = container.NewStack()
	continueBtn := widget.NewButton(host.continueLabel, nil)
	continueBtn.Importance = widget.HighImportance
	continueBtn.OnTapped = tr.applyAndContinue

	// exitBtn mirrors the caller's own escape hatch (Sync Recorders' Exit
	// Sync, Manage Files' Cancel): it leaves without applying any of the
	// corrections being reviewed here, same as bypassing the check
	// entirely - it deliberately calls host.onExit directly, not
	// applyAndContinue or host.onContinue, neither of which it wants to run.
	// Warns first, since it's easy to tap expecting the usual "leave"
	// behavior without registering that anything typed into the review is
	// about to be silently discarded.
	exitBtn := widget.NewButton(host.exitLabel, func() {
		showDangerConfirm("Corrections not applied", host.exitWarning,
			host.exitLabel, "Return to Review", func(ok bool) {
				if ok {
					host.onExit()
				}
			}, host.win)
	})
	exitBtn.Importance = widget.DangerImportance

	header := widget.NewLabelWithStyle("Review Recorder Timestamps", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	sub := widget.NewLabel("Each recorder's clock is assumed wrong (or right) for its entire session - adjusting one applies the same correction to every file from that recorder.")
	sub.Wrapping = fyne.TextWrapWord

	left := container.NewBorder(sectionHeader("Recorders"), nil, nil, nil, container.NewVScroll(leftBox))
	right := container.NewBorder(nil, nil, nil, nil, tr.detailBox)
	split := container.NewHSplit(left, right)
	split.SetOffset(0.3)

	content := container.NewBorder(
		container.NewVBox(header, sub, widget.NewSeparator()),
		container.NewHBox(continueBtn, exitBtn),
		nil, nil,
		split,
	)
	host.s.setContent(container.NewPadded(content))

	tr.selectRow(0)
}

// selectRow switches the detail pane to entries[i] and refreshes every left
// card's highlight so the selected one reads clearly against the rest.
func (tr *timestampReviewScreen) selectRow(i int) {
	tr.selected = i
	for j, bg := range tr.cards {
		if j == i {
			bg.StrokeColor = theme.Color(theme.ColorNamePrimary)
			bg.StrokeWidth = 3
		} else {
			bg.StrokeWidth = 0
		}
		bg.Refresh()
	}
	tr.rebuildDetail()
}

// rebuildDetail redraws the right-hand pane for the currently selected
// recorder: its Adjust checkbox and correction entry, plus a live rename
// preview for every one of its files - the old name struck through and the
// new one beside it, recomputed from whatever's currently in the entry.
func (tr *timestampReviewScreen) rebuildDetail() {
	e := tr.entries[tr.selected]

	header := widget.NewLabelWithStyle(
		fmt.Sprintf("%s — %s", e.row.recorderID, timestampIssueLabel(e.row.check.Kind)),
		fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	entry := widget.NewEntry()
	entry.SetText(e.text)

	errLbl := widget.NewLabel("")
	errLbl.Wrapping = fyne.TextWrapWord

	previewLbl := widget.NewLabel("")
	adjustLbl := widget.NewLabel("")
	refreshPreview := func() {
		if !e.adjust {
			previewLbl.SetText("")
			adjustLbl.SetText("")
			return
		}
		edited, err := time.ParseInLocation("2006-01-02 15:04", e.text, e.row.check.Recorded.Location())
		if err != nil {
			previewLbl.SetText("")
			adjustLbl.SetText("")
			return
		}
		previewLbl.SetText("New start time: " + plainDateTime(edited))
		adjustLbl.SetText("Adjustment: " + formatAdjustment(e.row.check.Recorded, edited))
	}

	filesBox := container.NewVBox()
	refreshFiles := func() {
		filesBox.Objects = nil
		for _, sf := range e.row.sourceFiles {
			oldT, ok := e.row.parser.ParseTimestamp(sf.DestRelPath)
			if !ok {
				filesBox.Add(widget.NewLabel(sf.DestRelPath))
				continue
			}
			if !e.adjust {
				filesBox.Add(widget.NewLabel(sf.DestRelPath))
				continue
			}
			edited, err := time.ParseInLocation("2006-01-02 15:04", e.text, e.row.check.Recorded.Location())
			if err != nil {
				filesBox.Add(widget.NewLabel(sf.DestRelPath))
				continue
			}
			delta := edited.Sub(e.row.check.Recorded)
			newT := oldT.Add(delta)
			newRel := e.row.parser.RenameForTimestamp(sf.DestRelPath, newT)
			oldLbl := widget.NewLabel(strikethrough(sf.DestRelPath))
			newLbl := widget.NewLabelWithStyle(newRel, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			oldCol := container.NewVBox(oldLbl, widget.NewLabel(plainDateTime(oldT)))
			newCol := container.NewVBox(newLbl, widget.NewLabel(plainDateTime(newT)))
			filesBox.Add(container.NewHBox(oldCol, widget.NewLabel("→"), newCol))
		}
		filesBox.Refresh()
	}

	entry.OnChanged = func(text string) {
		e.text = text
		errLbl.SetText("")
		refreshPreview()
		refreshFiles()
	}

	adjust := widget.NewCheck("New start time", nil)
	adjust.SetChecked(e.adjust)
	setEnabled := func(on bool) {
		if on {
			entry.Enable()
		} else {
			entry.Disable()
		}
	}
	setEnabled(e.adjust)
	adjust.OnChanged = func(checked bool) {
		e.adjust = checked
		setEnabled(checked)
		refreshPreview()
		refreshFiles()
		tr.cards[tr.selected].FillColor = timestampCardColorFor(e)
		tr.cards[tr.selected].Refresh()
	}

	refreshPreview()
	refreshFiles()

	detail := container.NewBorder(
		container.NewVBox(header, container.NewBorder(nil, nil, adjust, nil, entry), previewLbl, adjustLbl, errLbl, widget.NewSeparator(),
			widget.NewLabelWithStyle(fmt.Sprintf("Files (%d)", len(e.row.sourceFiles)), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})),
		nil, nil, nil,
		container.NewVScroll(filesBox),
	)

	tr.detailBox.Objects = []fyne.CanvasObject{container.NewPadded(detail)}
	tr.detailBox.Refresh()
}

// applyAndContinue validates every checked entry's correction text, applies
// them all (renaming every file for that recorder - see
// recorder.ApplyTimestampFix - and re-uploading outside batch mode), then
// hands off to onContinue (Batch Upload or End Sync).
func (tr *timestampReviewScreen) applyAndContinue() {
	type parsedFix struct {
		entry *timestampReviewEntry
		delta time.Duration
	}
	var fixes []parsedFix
	for _, e := range tr.entries {
		if !e.adjust {
			continue
		}
		edited, err := time.ParseInLocation("2006-01-02 15:04", e.text, e.row.check.Recorded.Location())
		if err != nil {
			tr.selectForEntry(e)
			return
		}
		delta := edited.Sub(e.row.check.Recorded)
		if delta == 0 {
			continue
		}
		fixes = append(fixes, parsedFix{e, delta})
	}

	for _, f := range fixes {
		_ = f.entry.row.apply(func(t time.Time) time.Time { return t.Add(f.delta) })
		if tr.host.afterFix != nil {
			tr.host.afterFix(f.entry.row, f.delta)
		}
	}
	tr.host.onContinue()
}

// selectForEntry switches the detail pane to e (used to surface a parse
// error on Apply without silently skipping that recorder's correction).
func (tr *timestampReviewScreen) selectForEntry(e *timestampReviewEntry) {
	for i, other := range tr.entries {
		if other == e {
			tr.selectRow(i)
			return
		}
	}
}

// tappableCard wraps arbitrary content in a fyne.Tappable, used for the
// review screen's selectable recorder cards - the same "colored background
// behind padded content" shape the active-sync screen's rows use, plus a
// tap handler.
type tappableCard struct {
	widget.BaseWidget
	content  fyne.CanvasObject
	onTapped func()
}

func newTappableCard(content fyne.CanvasObject, onTapped func()) *tappableCard {
	c := &tappableCard{content: content, onTapped: onTapped}
	c.ExtendBaseWidget(c)
	return c
}

func (c *tappableCard) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(c.content)
}

func (c *tappableCard) Tapped(*fyne.PointEvent) {
	if c.onTapped != nil {
		c.onTapped()
	}
}

// reuploadCorrectedFiles pushes every one of row's files - renamed by delta,
// see showTimestampReview - to every one of sc.params.uploads. Needed only
// outside batch-upload mode: there, each file already uploaded under its
// (bad) name the instant it landed locally during the sync itself (see
// StartOffload), so the corrected local copy never gets uploaded on its own
// unless this pushes it. The stale, wrongly-named remote copy is left in
// place rather than deleted, per this app's never-delete rule (see
// CLAUDE.md) - the user can clean it up manually once the correctly-named
// copy is confirmed uploaded. destDirs is row's recorder's local
// destinations (Sync Recorders-only - passed in rather than read off row
// since timestampReviewRow itself no longer carries it, see
// checkTimestampsThen's destDirsByID).
func reuploadCorrectedFiles(sc *recorderSyncScreen, row timestampReviewRow, destDirs []string, delta time.Duration) {
	if len(destDirs) == 0 {
		return
	}
	subpathParts := splitSubpathUI(sc.params.subpath)
	for _, sf := range row.sourceFiles {
		t, ok := row.parser.ParseTimestamp(sf.DestRelPath)
		if !ok {
			continue
		}
		newRel := row.parser.RenameForTimestamp(sf.DestRelPath, t.Add(delta))
		localPath := filepath.Join(destDirs[0], newRel)
		relParts := append([]string{sc.params.experimentName}, subpathParts...)
		relParts = append(relParts, row.recorderID, newRel)
		relPath := filepath.Join(relParts...)
		for _, dest := range sc.params.uploads {
			dest := dest
			go recorder.UploadCorrectedFile(sc.watchCtx, row.recorderID, relPath, localPath, dest, sc.uploads.onUploadEvent)
		}
	}
}
