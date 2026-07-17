package ui

import (
	"fmt"
	"image/color"
	"path"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// destructiveRed is used for anything that warns about a real, irreversible
// deletion — see NWayDelete's doc comment on why deletion exists at all
// here despite the app's otherwise-absolute never-delete-from-remotes rule.
var destructiveRed = color.NRGBA{R: 220, G: 38, B: 38, A: 255}

// conflictRow is one file flagged by the size+prefix check (see
// syncengine.compareObjects) as neither a confident copy nor a confident
// skip — the source and destination collide on relative path but don't
// agree on both size and leading bytes, so the app won't guess which one
// is right.
type conflictRow struct {
	TaskLabel string
	RelPath   string
	SrcSize   int64
	DstSize   int64
	Reason    string
}

// collectConflicts gathers every ActionConflict entry across a set of scan
// results, tagged with which task (experiment/destination pairing) each
// came from.
func collectConflicts(tasks []scanTask, results []syncengine.ScanResult) []conflictRow {
	var conflicts []conflictRow
	for i, result := range results {
		label := ""
		if i < len(tasks) {
			label = tasks[i].Label
		}
		for _, entry := range result.Entries {
			if entry.Action != syncengine.ActionConflict {
				continue
			}
			conflicts = append(conflicts, conflictRow{
				TaskLabel: label,
				RelPath:   entry.RelPath,
				SrcSize:   entry.Size,
				DstSize:   entry.DstSize,
				Reason:    entry.ConflictReason,
			})
		}
	}
	return conflicts
}

// showConflictsPrompt lists every file the scan couldn't confidently
// classify as copy-or-skip and asks the user to confirm before syncing.
// Conflicting files are always excluded from the copy itself (see
// filesFromFilter) — this dialog is purely a heads-up so a partial upload
// or a genuinely different same-path recording never gets silently left
// out without the user knowing. "Sync anyway" proceeds with everything
// else; "Cancel" returns to the scan results untouched so the user can
// investigate a conflict manually before deciding.
func showConflictsPrompt(s *state, conflicts []conflictRow, onProceed func()) {
	const maxRows = 200

	list := container.NewVBox()
	shown := conflicts
	truncated := false
	if len(shown) > maxRows {
		shown = shown[:maxRows]
		truncated = true
	}
	for _, c := range shown {
		title := c.RelPath
		if c.TaskLabel != "" {
			title = fmt.Sprintf("%s  (%s)", c.RelPath, c.TaskLabel)
		}
		titleLabel := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		detail := widget.NewLabel(fmt.Sprintf("%s — source %s, destination %s",
			c.Reason, humanBytes(c.SrcSize), humanBytes(c.DstSize)))
		detail.Wrapping = fyne.TextWrapWord
		list.Add(container.NewVBox(titleLabel, detail))
	}
	if truncated {
		list.Add(widget.NewLabel(fmt.Sprintf("…and %d more", len(conflicts)-maxRows)))
	}

	scroll := container.NewVScroll(list)
	scroll.SetMinSize(fyne.NewSize(520, 320))

	header := widget.NewLabel(fmt.Sprintf(
		"%d file(s) couldn't be confidently matched between source and destination.\n"+
			"These will NOT be copied — review them, then choose how to proceed.",
		len(conflicts)))
	header.Wrapping = fyne.TextWrapWord

	var d dialog.Dialog
	proceedBtn := widget.NewButton("Sync anyway (skip conflicts)", func() {
		d.Hide()
		onProceed()
	})
	proceedBtn.Importance = widget.WarningImportance
	cancelBtn := widget.NewButton("Cancel", func() { d.Hide() })

	footer := container.NewCenter(container.NewHBox(proceedBtn, cancelBtn))
	d = dialog.NewCustomWithoutButtons("Conflicts found", container.NewBorder(header, footer, nil, nil, scroll), s.win)
	d.Resize(fyne.NewSize(560, 420))
	d.Show()
}

// nwayConflictKey identifies one conflicting file across a whole N-way scan
// session (relPath alone can repeat across experiments).
type nwayConflictKey struct {
	expName string
	relPath string
}

// nwayVersion is one location's copy of a conflicting file.
type nwayVersion struct {
	loc     syncengine.Location
	size    int64
	modTime time.Time
}

// nwayConflict is one file the N-way scan couldn't confidently converge
// (see syncengine.compareObjectsN / FileConflict), with every location's
// copy of it — the unit the resolver steps through.
type nwayConflict struct {
	key      nwayConflictKey
	reason   string
	versions []nwayVersion
}

// nwayChoiceKind is the user's per-conflict decision in the resolver.
// nwayChoiceNone means undecided — Sync stays unreachable until every
// conflict has an explicit choice (there is deliberately no default).
type nwayChoiceKind int

const (
	nwayChoiceNone nwayChoiceKind = iota
	// nwayChoiceKeepOne: one location's version becomes canonical and
	// overwrites every other copy (syncengine.NWayOverwrite).
	nwayChoiceKeepOne
	// nwayChoiceKeepAll: every location's copy is renamed to its own
	// location-tagged name (syncengine.NWayRename), then each renamed file
	// propagates everywhere like any other new file. Nothing is discarded.
	nwayChoiceKeepAll
	// nwayChoiceSkip: leave every copy untouched and sync around this file
	// (syncengine.NWayIgnore) — a deliberate choice here, never a fallback.
	nwayChoiceSkip
	// nwayChoiceDelete: permanently delete the chosen locations' copies
	// (syncengine.NWayDelete) — see that constant's doc comment; gated
	// behind its own irreversible-action confirmation before Sync runs.
	nwayChoiceDelete
)

type nwayChoice struct {
	kind      nwayChoiceKind
	winner    syncengine.Location   // keep-one only
	deleteLoc []syncengine.Location // delete only
}

// decided reports whether this choice fully resolves its conflict.
func (c nwayChoice) decided() bool {
	switch c.kind {
	case nwayChoiceNone:
		return false
	case nwayChoiceKeepOne:
		return c.winner.ID != ""
	case nwayChoiceDelete:
		return len(c.deleteLoc) > 0
	default:
		return true
	}
}

// nwayResolver holds one N-way scan session's conflict-resolution state:
// the per-experiment scan results as they complete, and the user's explicit
// per-conflict choices. The sync-flow screen consults it to gate Sync
// (unresolvedCount must reach zero) and to caption conflict rows; the
// resolver dialog (showNWayResolveDialog) edits it.
type nwayResolver struct {
	expNames []string
	results  []syncengine.NWayScanResult // index-aligned with expNames; each filled in when that experiment's scan completes
	choices  map[nwayConflictKey]nwayChoice
	onChange func() // set by the sync-flow screen: refreshes rows and the Sync/Resolve buttons after any choice changes
}

func newNWayResolver(expNames []string) *nwayResolver {
	return &nwayResolver{
		expNames: expNames,
		results:  make([]syncengine.NWayScanResult, len(expNames)),
		choices:  map[nwayConflictKey]nwayChoice{},
	}
}

// conflicts returns every conflict across every scanned experiment, in
// experiment order then file order — the resolver's stepping order.
func (r *nwayResolver) conflicts() []nwayConflict {
	var out []nwayConflict
	for i, result := range r.results {
		for _, f := range result.Files {
			if f.Status != syncengine.FileConflict {
				continue
			}
			c := nwayConflict{
				key:    nwayConflictKey{expName: r.expNames[i], relPath: f.RelPath},
				reason: f.ConflictReason,
			}
			for _, st := range f.States {
				if st.Exists {
					c.versions = append(c.versions, nwayVersion{loc: st.Location, size: st.Size, modTime: st.ModTime})
				}
			}
			out = append(out, c)
		}
	}
	return out
}

func (r *nwayResolver) conflictCount() int {
	return len(r.conflicts())
}

func (r *nwayResolver) unresolvedCount() int {
	n := 0
	for _, c := range r.conflicts() {
		if !r.choices[c.key].decided() {
			n++
		}
	}
	return n
}

func (r *nwayResolver) setChoice(key nwayConflictKey, choice nwayChoice) {
	r.choices[key] = choice
	if r.onChange != nil {
		r.onChange()
	}
}

// rowSummary captions one conflict's file row in the sync-flow screen:
// the warning plus reason while undecided, the decision once made — so the
// main screen doubles as the review surface before Sync.
func (r *nwayResolver) rowSummary(expName, relPath, reason string) string {
	choice := r.choices[nwayConflictKey{expName: expName, relPath: relPath}]
	if !choice.decided() {
		return fmt.Sprintf("⚠ conflict — %s", reason)
	}
	switch choice.kind {
	case nwayChoiceKeepOne:
		return fmt.Sprintf("✓ keeping %s's version", choice.winner.Name)
	case nwayChoiceKeepAll:
		return "✓ keeping all versions (renamed)"
	case nwayChoiceSkip:
		return "— not syncing"
	default:
		names := make([]string, len(choice.deleteLoc))
		for i, loc := range choice.deleteLoc {
			names[i] = loc.Name
		}
		return fmt.Sprintf("✗ deleting from %s", strings.Join(names, ", "))
	}
}

// hasDeletes reports whether any decided choice deletes a copy — callers
// must gate Sync behind showIrreversibleDeleteConfirm when true.
func (r *nwayResolver) hasDeletes() bool {
	for _, choice := range r.choices {
		if choice.kind == nwayChoiceDelete && choice.decided() {
			return true
		}
	}
	return false
}

// hasActionable reports whether any decided choice requires real work
// (overwrite, rename, delete) — used to enable Sync even when the scan
// found nothing else to copy.
func (r *nwayResolver) hasActionable() bool {
	for _, choice := range r.choices {
		if choice.decided() && choice.kind != nwayChoiceSkip {
			return true
		}
	}
	return false
}

// buildResolutions converts every decided choice into engine resolutions.
// Callers gate on unresolvedCount() == 0 first, so nothing is silently
// defaulted here. A keep-all choice emits one rename per present copy, each
// with its own location-tagged name (see SuggestConflictRenameNameAt) so
// differing copies can never collide under one new name.
func (r *nwayResolver) buildResolutions() []syncengine.NWayConflictResolution {
	var out []syncengine.NWayConflictResolution
	for _, c := range r.conflicts() {
		choice := r.choices[c.key]
		switch choice.kind {
		case nwayChoiceKeepOne:
			out = append(out, syncengine.NWayConflictResolution{
				ExpName:          c.key.expName,
				RelPath:          c.key.relPath,
				Kind:             syncengine.NWayOverwrite,
				WinnerLocationID: choice.winner.ID,
			})
		case nwayChoiceKeepAll:
			for _, v := range c.versions {
				out = append(out, syncengine.NWayConflictResolution{
					ExpName:           c.key.expName,
					RelPath:           c.key.relPath,
					Kind:              syncengine.NWayRename,
					TargetLocationIDs: []string{v.loc.ID},
					NewName:           syncengine.SuggestConflictRenameNameAt(c.key.relPath, v.loc.Name),
				})
			}
		case nwayChoiceSkip:
			out = append(out, syncengine.NWayConflictResolution{
				ExpName: c.key.expName,
				RelPath: c.key.relPath,
				Kind:    syncengine.NWayIgnore,
			})
		case nwayChoiceDelete:
			ids := make([]string, len(choice.deleteLoc))
			for i, loc := range choice.deleteLoc {
				ids[i] = loc.ID
			}
			out = append(out, syncengine.NWayConflictResolution{
				ExpName:           c.key.expName,
				RelPath:           c.key.relPath,
				Kind:              syncengine.NWayDelete,
				TargetLocationIDs: ids,
			})
		}
	}
	return out
}

// applyChoiceToUnresolved bulk-applies choice to every conflict that is
// still undecided (the current one included) and where it is applicable:
// keep-one only applies where the winner location actually has a copy.
// Deletes are deliberately excluded from bulk application — an irreversible
// action stays a per-file decision.
func (r *nwayResolver) applyChoiceToUnresolved(choice nwayChoice) {
	if !choice.decided() || choice.kind == nwayChoiceDelete {
		return
	}
	for _, c := range r.conflicts() {
		if r.choices[c.key].decided() {
			continue
		}
		if choice.kind == nwayChoiceKeepOne {
			present := false
			for _, v := range c.versions {
				if v.loc.ID == choice.winner.ID {
					present = true
					break
				}
			}
			if !present {
				continue
			}
		}
		r.choices[c.key] = choice
	}
	if r.onChange != nil {
		r.onChange()
	}
}

// showNWayResolveDialog is the per-file conflict resolver: one conflict at a
// time, framed as "which version do you keep?" — every present copy is a
// radio option (with its size and modified time, so a stale partial upload
// is visibly distinguishable), plus keep-all, don't-sync, and delete. Prev/
// Next step through every conflict; startAt (may be nil) jumps straight to
// one conflict when the user clicked its row. Closing early is fine — Sync
// stays gated until every conflict is decided.
func showNWayResolveDialog(s *state, r *nwayResolver, startAt *nwayConflictKey) {
	conflicts := r.conflicts()
	if len(conflicts) == 0 {
		return
	}
	idx := 0
	if startAt != nil {
		for i, c := range conflicts {
			if c.key == *startAt {
				idx = i
				break
			}
		}
	} else {
		// Opened from the Resolve button: land on the first conflict still
		// needing a decision.
		for i, c := range conflicts {
			if !r.choices[c.key].decided() {
				idx = i
				break
			}
		}
	}

	posLabel := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	pathLabel := widget.NewLabel("")
	pathLabel.Truncation = fyne.TextTruncateEllipsis
	reasonLabel := widget.NewLabel("")
	reasonLabel.Wrapping = fyne.TextWrapWord

	radio := widget.NewRadioGroup(nil, nil)
	subArea := container.NewVBox()
	applyAllCheck := widget.NewCheck("Apply this choice to every unresolved conflict", nil)
	resolvedLabel := widget.NewLabel("")
	prevBtn := widget.NewButton("← Previous", nil)
	nextBtn := widget.NewButton("Next →", nil)

	// rendering suppresses the radio/check OnChanged callbacks while render()
	// rebuilds the widgets for a newly shown conflict.
	rendering := false

	const (
		optKeepAll = "Keep all versions — rename each location's copy, sync every one everywhere"
		optSkip    = "Don't sync these files — leave every copy where it is"
		optDelete  = "Delete this file from chosen locations…"
	)

	var render func()

	// current returns the conflict the dialog is showing.
	current := func() nwayConflict { return conflicts[idx] }

	versionLabel := func(v nwayVersion) string {
		when := ""
		if !v.modTime.IsZero() {
			when = ", " + v.modTime.Format("Jan 2 2006 15:04")
		}
		return fmt.Sprintf("Keep %s's version  (%s%s)", v.loc.Name, humanBytes(v.size), when)
	}

	// choiceFromSelection maps the radio selection (plus the delete
	// checkgroup, for delete) back to a choice for the current conflict.
	choiceFromSelection := func(selected string, deleteSel []syncengine.Location) nwayChoice {
		switch selected {
		case optKeepAll:
			return nwayChoice{kind: nwayChoiceKeepAll}
		case optSkip:
			return nwayChoice{kind: nwayChoiceSkip}
		case optDelete:
			return nwayChoice{kind: nwayChoiceDelete, deleteLoc: deleteSel}
		case "":
			return nwayChoice{}
		}
		for _, v := range current().versions {
			if versionLabel(v) == selected {
				return nwayChoice{kind: nwayChoiceKeepOne, winner: v.loc}
			}
		}
		return nwayChoice{}
	}

	// renderSubArea fills the area under the radio group: the concrete new
	// names for keep-all, or the destructive warning + location picker for
	// delete.
	renderSubArea := func(choice nwayChoice) {
		subArea.Objects = nil
		switch choice.kind {
		case nwayChoiceKeepAll:
			for _, v := range current().versions {
				preview := widget.NewLabel(fmt.Sprintf("%s's copy → %s", v.loc.Name,
					syncengine.SuggestConflictRenameNameAt(current().key.relPath, v.loc.Name)))
				preview.Truncation = fyne.TextTruncateEllipsis
				subArea.Add(preview)
			}
		case nwayChoiceDelete:
			warning := canvas.NewText("Delete is permanent and cannot be undone.", destructiveRed)
			warning.TextStyle = fyne.TextStyle{Bold: true}
			subArea.Add(warning)

			c := current()
			names := make([]string, len(c.versions))
			locByName := make(map[string]syncengine.Location, len(c.versions))
			for i, v := range c.versions {
				names[i] = v.loc.Name
				locByName[v.loc.Name] = v.loc
			}
			checked := make([]string, len(choice.deleteLoc))
			for i, loc := range choice.deleteLoc {
				checked[i] = loc.Name
			}
			group := widget.NewCheckGroup(names, func(sel []string) {
				if rendering {
					return
				}
				locs := make([]syncengine.Location, 0, len(sel))
				for _, n := range sel {
					locs = append(locs, locByName[n])
				}
				r.setChoice(current().key, nwayChoice{kind: nwayChoiceDelete, deleteLoc: locs})
				render()
			})
			group.Selected = checked
			subArea.Add(group)
		}
		subArea.Refresh()
	}

	render = func() {
		rendering = true
		defer func() { rendering = false }()

		c := current()
		choice := r.choices[c.key]

		posLabel.SetText(fmt.Sprintf("Conflict %d of %d", idx+1, len(conflicts)))
		title := path.Base(c.key.relPath)
		dir := path.Dir(c.key.relPath)
		if dir == "." {
			dir = ""
		} else {
			dir = "  in " + dir
		}
		pathLabel.SetText(fmt.Sprintf("%s%s  (%s)", title, dir, c.key.expName))
		reasonLabel.SetText(c.reason)

		options := make([]string, 0, len(c.versions)+3)
		for _, v := range c.versions {
			options = append(options, versionLabel(v))
		}
		options = append(options, optKeepAll, optSkip, optDelete)
		radio.Options = options

		selected := ""
		switch choice.kind {
		case nwayChoiceKeepOne:
			for _, v := range c.versions {
				if v.loc.ID == choice.winner.ID {
					selected = versionLabel(v)
				}
			}
		case nwayChoiceKeepAll:
			selected = optKeepAll
		case nwayChoiceSkip:
			selected = optSkip
		case nwayChoiceDelete:
			selected = optDelete
		}
		radio.Selected = selected
		radio.Refresh()

		renderSubArea(choice)

		resolved := len(conflicts) - r.unresolvedCount()
		resolvedLabel.SetText(fmt.Sprintf("%d of %d resolved", resolved, len(conflicts)))

		// Bulk application never includes deletes (see
		// applyChoiceToUnresolved), so the checkbox goes dark there.
		if choice.kind == nwayChoiceDelete {
			applyAllCheck.SetChecked(false)
			applyAllCheck.Disable()
		} else {
			applyAllCheck.Enable()
		}

		if idx > 0 {
			prevBtn.Enable()
		} else {
			prevBtn.Disable()
		}
		if idx < len(conflicts)-1 {
			nextBtn.Enable()
		} else {
			nextBtn.Disable()
		}
	}

	radio.OnChanged = func(selected string) {
		if rendering {
			return
		}
		choice := choiceFromSelection(selected, r.choices[current().key].deleteLoc)
		if choice.kind == nwayChoiceDelete {
			// Selecting delete keeps any previously chosen locations but
			// never inherits them from another kind of choice.
			prev := r.choices[current().key]
			if prev.kind != nwayChoiceDelete {
				choice.deleteLoc = nil
			}
		}
		r.setChoice(current().key, choice)
		if applyAllCheck.Checked && choice.decided() && choice.kind != nwayChoiceDelete {
			r.applyChoiceToUnresolved(choice)
		}
		render()
	}
	applyAllCheck.OnChanged = func(checked bool) {
		if rendering || !checked {
			return
		}
		if choice := r.choices[current().key]; choice.decided() {
			r.applyChoiceToUnresolved(choice)
			render()
		}
	}
	prevBtn.OnTapped = func() {
		if idx > 0 {
			idx--
			render()
		}
	}
	nextBtn.OnTapped = func() {
		if idx < len(conflicts)-1 {
			idx++
			render()
		}
	}

	header := container.NewVBox(posLabel, pathLabel, reasonLabel, widget.NewSeparator())
	body := container.NewVScroll(container.NewVBox(radio, subArea))
	body.SetMinSize(fyne.NewSize(600, 260))
	footer := container.NewVBox(
		applyAllCheck,
		container.NewBorder(nil, nil, resolvedLabel, container.NewHBox(prevBtn, nextBtn)),
	)

	render()

	d := dialog.NewCustom("Resolve conflicts", "Done", container.NewBorder(header, footer, nil, nil, body), s.win)
	d.Resize(fyne.NewSize(660, 500))
	d.Show()
}

// showIrreversibleDeleteConfirm gates any resolution that deletes a file
// behind its own explicit confirmation naming the fact that it cannot be
// undone — separate from the main Sync/Apply action so a delete is never
// one click away from an otherwise-routine operation. message and
// confirmLabel let each caller name what's actually being deleted (e.g. a
// file count) and what confirming actually does (N-way conflict resolution
// continues into a sync; Manage Files just deletes).
func showIrreversibleDeleteConfirm(s *state, message, confirmLabel string, onConfirm func()) {
	msg := canvas.NewText(message, destructiveRed)
	msg.TextStyle = fyne.TextStyle{Bold: true}
	d := dialog.NewCustomConfirm("Delete is irreversible", confirmLabel, "Cancel",
		container.NewVBox(msg), func(confirmed bool) {
			if confirmed {
				onConfirm()
			}
		}, s.win)
	d.SetConfirmImportance(widget.DangerImportance)
	d.Show()
}
