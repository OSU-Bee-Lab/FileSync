package ui

import (
	"context"
	"fmt"
	"path"
	"sort"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// showSyncExperiments is the Sync Experiments flow: N-way, always
// whole-experiment granularity. There's no From/To distinction and no
// designated source — pick two or more locations and one or more
// experiments, and every selected location converges on every file, with
// same-name/different-content disagreements always surfaced for an
// explicit decision (never guessed; see syncengine.compareObjectsN). This
// supersedes the old pairwise/directional flow: with exactly two locations
// selected it behaves the same as the old From/To sync, since N-way
// diffing degenerates cleanly to a two-way diff.
func showSyncExperiments(s *state) {
	allNames := locationNames(s.cfg.Locations)
	statusLabel := widget.NewLabel("Pick two or more locations and at least one experiment.")

	locGroup := newToggleGroup(allNames, append([]string{}, s.syncExperimentsLocationNames...))
	expGroup := widget.NewCheckGroup(nil, nil)

	var scanBtn *widget.Button
	updateScanBtn := func() {
		if scanBtn == nil {
			return
		}
		if len(locGroup.Selected()) >= 2 && len(expGroup.Selected) > 0 {
			scanBtn.Enable()
		} else {
			scanBtn.Disable()
		}
	}
	expGroup.OnChanged = func(sel []string) {
		s.syncExperimentsExpNames = sel
		updateScanBtn()
	}

	// refresh reloads the experiment list as the union of every experiment
	// visible from any of the currently-selected locations — a location
	// missing an experiment entirely is exactly the "hasn't arrived there
	// yet" case this flow exists to fill in, so it must still be offered.
	refresh := func() {
		names := locGroup.Selected()
		if len(names) < 2 {
			expGroup.Options = nil
			expGroup.Selected = nil
			expGroup.Refresh()
			updateScanBtn()
			statusLabel.SetText("Pick two or more locations and at least one experiment.")
			return
		}
		locs := locationsFromNamesAny(s.cfg.Locations, names)
		statusLabel.SetText("Loading experiments...")
		expGroup.Options = nil
		expGroup.Selected = nil
		expGroup.Refresh()
		updateScanBtn()

		go func() {
			ctx := context.Background()
			seen := map[string]bool{}
			var union []string
			var firstErr error
			for _, loc := range locs {
				exps, err := syncengine.ListExperiments(ctx, loc)
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				for _, e := range exps {
					if !seen[e.Name] {
						seen[e.Name] = true
						union = append(union, e.Name)
					}
				}
			}
			sort.Strings(union)

			fyne.Do(func() {
				if !equalStringSets(locGroup.Selected(), names) {
					return // selection changed mid-load; a newer refresh is in flight
				}
				expGroup.Options = union
				keep := make([]string, 0, len(s.syncExperimentsExpNames))
				for _, name := range s.syncExperimentsExpNames {
					if seen[name] {
						keep = append(keep, name)
					}
				}
				expGroup.Selected = keep
				s.syncExperimentsExpNames = keep
				expGroup.Refresh()
				updateScanBtn()
				if firstErr != nil {
					statusLabel.SetText(fmt.Sprintf("%d experiment(s) found (one or more locations failed to list: %v)", len(union), firstErr))
				} else {
					statusLabel.SetText(fmt.Sprintf("%d experiment(s) found across %d location(s)", len(union), len(locs)))
				}
			})
		}()
	}

	locGroup.OnChanged = func(sel []string) {
		s.syncExperimentsLocationNames = sel
		refresh()
	}
	if len(locGroup.Selected()) >= 2 {
		refresh()
	}

	scanBtn = widget.NewButton("Scan", func() {
		names := locGroup.Selected()
		expNames := append([]string{}, expGroup.Selected...)
		if len(names) < 2 {
			dialog.ShowInformation("Pick locations", "Choose at least two locations to converge.", s.win)
			return
		}
		if len(expNames) == 0 {
			dialog.ShowInformation("Pick experiments", "Select at least one experiment to sync.", s.win)
			return
		}
		locs := locationsFromNamesAny(s.cfg.Locations, names)

		startScan := func() {
			runNWayScan(s, locs, expNames)
		}

		if missing := missingLocalLocations(locs...); len(missing) > 0 {
			showLocationsNotFoundPrompt(s, missing, func() {
				keep := make([]string, 0, len(names))
				for _, name := range names {
					if loc := findLocation(s.cfg.Locations, name); loc == nil || !containsLocation(missing, *loc) {
						keep = append(keep, name)
					}
				}
				locGroup.SetSelected(keep)
				s.syncExperimentsLocationNames = keep
				updateScanBtn()
			}, startScan)
			return
		}
		startScan()
	})
	scanBtn.Importance = widget.HighImportance
	updateScanBtn()
	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Sync Experiments", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabel("Pick two or more locations to converge — no location is a designated source."),
			container.NewPadded(locGroup.CanvasObject()),
			statusLabel,
			widget.NewSeparator(),
		),
		container.NewHBox(scanBtn, backBtn),
		nil, nil,
		container.NewVScroll(expGroup),
	)
	s.setContent(container.NewPadded(content))
}

// runNWayScan runs the N-way scan live inside the shared scan/sync screen:
// one task per experiment, each diffing that experiment across every
// selected location (syncengine.ScanNWayWithProgress) with the same
// three-column live progress as a pairwise scan — conflicts surface the
// moment they're found, not in a blocking wait dialog at the end. Sync is
// gated behind an explicit per-file resolution for every conflict (see
// nwayResolver); pressing it applies the resolutions and hands the
// resulting transfer plan to runNWayTransfers.
func runNWayScan(s *state, locs []syncengine.Location, expNames []string) {
	fset := s.cfg.DefaultFilter
	preserveModTime := s.cfg.PreserveModTime

	resolver := newNWayResolver(expNames)

	tasks := make([]scanTask, len(expNames))
	for i, name := range expNames {
		i, name := i, name
		tasks[i] = scanTask{
			Label: name,
			Locs:  locs,
			Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
				result, err := syncengine.ScanNWayWithProgress(ctx, locs, name, fset, progress)
				if err != nil {
					return syncengine.ScanResult{}, err
				}
				resolver.results[i] = result
				return syncengine.NWayDisplayScanResult(result), nil
			},
			// Start is deliberately nil: this session's Sync is replaced by
			// onNWaySync below, which builds and runs the real per-pair
			// transfer plan in a fresh session.
		}
	}

	onSync := func() {
		resolutions := resolver.buildResolutions()
		proceed := func() {
			applyNWayResolutions(s, expNames, resolver.results, locs, fset, resolutions, func(resolved []syncengine.NWayScanResult) {
				runNWayTransfers(s, expNames, resolved, fset, preserveModTime)
			})
		}
		if resolver.hasDeletes() {
			showIrreversibleDeleteConfirm(s, proceed)
			return
		}
		proceed()
	}

	showSyncFlowExtras(s, tasks, func() { showSyncExperiments(s) },
		syncFlowExtras{nway: resolver, onNWaySync: onSync})
}

// runNWayTransfers builds the minimal transfer plan for every experiment
// and hands the resulting (source, dest, files) jobs to the existing
// scan/progress UI. The plan was already reviewed and confirmed in the scan
// session, so the transfer session auto-starts copying instead of asking
// for a second Sync press.
func runNWayTransfers(s *state, expNames []string, results []syncengine.NWayScanResult, fset syncengine.FilterSettings, preserveModTime bool) {
	var tasks []scanTask
	for i, name := range expNames {
		name := name
		pairs := syncengine.BuildNWayTransferPlan(results[i], syncengine.PreferLocalSource)
		for _, pair := range pairs {
			pair := pair
			result := syncengine.ScanResultFromNWayTransfers(pair)
			tasks = append(tasks, scanTask{
				Label: fmt.Sprintf("%s: %s → %s", name, pair.Source.Name, pair.Dest.Name),
				Locs:  []syncengine.Location{pair.Source, pair.Dest},
				Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
					return result, nil
				},
				Start: func(ctx context.Context, expected syncengine.ScanResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
					return syncengine.StartSyncExperiments(ctx, pair.Source, pair.Dest, name, fset, preserveModTime, expected)
				},
			})
		}
	}
	if len(tasks) == 0 {
		dialog.ShowInformation("Nothing to sync", "Every selected location already agrees on every file (excluding any files you chose not to sync).", s.win)
		return
	}
	showSyncFlowExtras(s, tasks, func() { showSyncExperiments(s) }, syncFlowExtras{autoSync: true})
}

// applyNWayResolutions executes any real Rename/Delete resolutions
// (physical rclone operations — see syncengine.RenameConflictFile/
// DeleteConflictFile), then re-scans every affected experiment fresh so
// the transfer plan reflects the new on-disk/on-remote state rather than a
// hand-patched guess, then applies Overwrite resolutions to the fresh scan
// and hands off to buildAndRun. If no resolution requires a physical
// operation, this skips straight to applying overwrites — no need to
// re-scan when nothing changed underneath.
func applyNWayResolutions(s *state, expNames []string, results []syncengine.NWayScanResult, locs []syncengine.Location, fset syncengine.FilterSettings, resolutions []syncengine.NWayConflictResolution, buildAndRun func([]syncengine.NWayScanResult)) {
	locByID := make(map[string]syncengine.Location, len(locs))
	for _, l := range locs {
		locByID[l.ID] = l
	}

	applyOverwrites := func(results []syncengine.NWayScanResult) []syncengine.NWayScanResult {
		resolved := make([]syncengine.NWayScanResult, len(results))
		for i, name := range expNames {
			var perExp []syncengine.NWayConflictResolution
			for _, r := range resolutions {
				if r.ExpName == name {
					perExp = append(perExp, r)
				}
			}
			resolved[i] = syncengine.ApplyOverwriteResolutions(results[i], perExp)
		}
		return resolved
	}

	hasPhysical := false
	for _, r := range resolutions {
		if (r.Kind == syncengine.NWayRename || r.Kind == syncengine.NWayDelete) && len(r.TargetLocationIDs) > 0 {
			hasPhysical = true
			break
		}
	}
	if !hasPhysical {
		buildAndRun(applyOverwrites(results))
		return
	}

	progressDlg := dialog.NewCustomWithoutButtons("Applying resolutions...", widget.NewLabel("Renaming/deleting conflicting files, please wait..."), s.win)
	progressDlg.Show()

	go func() {
		ctx := context.Background()
		var applyErr error
	applyLoop:
		for _, r := range resolutions {
			if len(r.TargetLocationIDs) == 0 {
				continue
			}
			fullPath := path.Join(r.ExpName, r.RelPath)
			switch r.Kind {
			case syncengine.NWayRename:
				newName := r.NewName
				if newName == "" {
					newName = syncengine.SuggestConflictRenameName(r.RelPath)
				}
				newFullPath := path.Join(path.Dir(fullPath), newName)
				for _, id := range r.TargetLocationIDs {
					loc, ok := locByID[id]
					if !ok {
						continue
					}
					if err := syncengine.RenameConflictFile(ctx, loc, fullPath, newFullPath); err != nil {
						applyErr = err
						break applyLoop
					}
				}
			case syncengine.NWayDelete:
				for _, id := range r.TargetLocationIDs {
					loc, ok := locByID[id]
					if !ok {
						continue
					}
					if err := syncengine.DeleteConflictFile(ctx, loc, fullPath); err != nil {
						applyErr = err
						break applyLoop
					}
				}
			}
		}

		if applyErr != nil {
			fyne.Do(func() {
				progressDlg.Hide()
				dialog.ShowError(applyErr, s.win)
			})
			return
		}

		freshResults := make([]syncengine.NWayScanResult, len(expNames))
		var scanErr error
		for i, name := range expNames {
			result, err := syncengine.ScanNWay(ctx, locs, name, fset)
			if err != nil {
				scanErr = fmt.Errorf("%s: %w", name, err)
				break
			}
			freshResults[i] = result
		}

		fyne.Do(func() {
			progressDlg.Hide()
			if scanErr != nil {
				dialog.ShowError(scanErr, s.win)
				return
			}
			buildAndRun(applyOverwrites(freshResults))
		})
	}()
}

// equalStringSets reports whether a and b contain the same set of strings,
// ignoring order (used to detect a stale async refresh whose selection has
// since changed).
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if !set[s] {
			return false
		}
	}
	return true
}
