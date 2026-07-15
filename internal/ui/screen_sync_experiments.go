package ui

import (
	"context"
	"fmt"
	"path"
	"sort"
	"sync"

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
	loading := newLoadingBar()

	locGroup := newToggleGroup(allNames, append([]string{}, s.syncExperimentsLocationNames...))
	expGroup := widget.NewCheckGroup(nil, nil)

	var quickScanBtn, fullScanBtn *widget.Button
	updateScanBtn := func() {
		if quickScanBtn == nil || fullScanBtn == nil {
			return
		}
		if len(locGroup.Selected()) >= 2 && len(expGroup.Selected) > 0 {
			quickScanBtn.Enable()
			fullScanBtn.Enable()
		} else {
			quickScanBtn.Disable()
			fullScanBtn.Disable()
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
		statusLabel.SetText("")
		loading.Show()
		expGroup.Options = nil
		expGroup.Selected = nil
		expGroup.Refresh()
		updateScanBtn()

		// applyUnion re-renders expGroup from the current union/seen state -
		// called both incrementally (as each location's listing lands, so a
		// fast location's experiments show right away instead of waiting on
		// a slow one) and once more at the end for the final status text.
		applyUnion := func(union []string, seen map[string]bool) {
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
		}

		go func() {
			ctx := context.Background()
			var mu sync.Mutex
			seen := map[string]bool{}
			var union []string
			var firstErr error
			var wg sync.WaitGroup
			for _, loc := range locs {
				wg.Add(1)
				go func(loc syncengine.Location) {
					defer wg.Done()
					exps, err := syncengine.ListExperiments(ctx, loc)
					mu.Lock()
					if err != nil {
						if firstErr == nil {
							firstErr = err
						}
						mu.Unlock()
						return
					}
					for _, e := range exps {
						if !seen[e.Name] {
							seen[e.Name] = true
							union = append(union, e.Name)
						}
					}
					sort.Strings(union)
					unionCopy := append([]string{}, union...)
					seenCopy := make(map[string]bool, len(seen))
					for k := range seen {
						seenCopy[k] = true
					}
					mu.Unlock()

					fyne.Do(func() {
						if !equalStringSets(locGroup.Selected(), names) {
							return // selection changed mid-load; a newer refresh is in flight
						}
						applyUnion(unionCopy, seenCopy)
					})
				}(loc)
			}
			wg.Wait()

			mu.Lock()
			finalUnion, finalErr := append([]string{}, union...), firstErr
			mu.Unlock()

			fyne.Do(func() {
				loading.Hide()
				if !equalStringSets(locGroup.Selected(), names) {
					return // selection changed mid-load; a newer refresh is in flight
				}
				if finalErr != nil {
					statusLabel.SetText(fmt.Sprintf("%d experiment(s) found (one or more locations failed to list: %v)", len(finalUnion), finalErr))
				} else {
					statusLabel.SetText(fmt.Sprintf("%d experiment(s) found across %d location(s)", len(finalUnion), len(locs)))
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

	startScanMode := func(mode syncengine.NWayScanMode) {
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
			runNWayScan(s, locs, expNames, mode)
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
	}

	quickScanBtn = widget.NewButton("Quick Scan", func() { startScanMode(syncengine.NWayQuickScan) })
	quickScanBtn.Importance = widget.HighImportance
	fullScanBtn = widget.NewButton("Full Scan", func() { startScanMode(syncengine.NWayFullScan) })
	updateScanBtn()
	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Sync Experiments", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewLabel("Pick two or more locations to converge — no location is a designated source."),
			container.NewPadded(locGroup.CanvasObject()),
			loading.CanvasObject(),
			statusLabel,
			widget.NewSeparator(),
		),
		container.NewHBox(quickScanBtn, fullScanBtn, backBtn),
		nil, nil,
		container.NewVScroll(expGroup),
	)
	s.setContent(container.NewPadded(content))
}

// runNWayScan runs the N-way scan live inside the shared scan/sync screen:
// one task per experiment, each diffing that experiment across every
// selected location (syncengine.ScanNWayWithProgress) with the same
// three-column live progress as a pairwise scan — conflicts surface the
// moment they're found, not in a blocking wait dialog at the end.
//
// Under NWayFullScan, Sync is gated behind an explicit per-file resolution
// for every conflict (see nwayResolver); pressing it applies the
// resolutions and hands the resulting transfer plan to runNWayTransfers.
//
// Under NWayQuickScan, diffNWay never reads bytes and so can never produce
// a conflict (see syncengine.NWayQuickScan) — no resolver is constructed at
// all, so the conflict-resolution UI never appears, and pressing Sync goes
// straight to building the transfer plan and handing it to
// runNWayTransfers.
func runNWayScan(s *state, locs []syncengine.Location, expNames []string, mode syncengine.NWayScanMode) {
	fset := s.cfg.DefaultFilter

	var resolver *nwayResolver
	if mode == syncengine.NWayFullScan {
		resolver = newNWayResolver(expNames)
	}
	results := make([]syncengine.NWayScanResult, len(expNames))

	tasks := make([]scanTask, len(expNames))
	for i, name := range expNames {
		tasks[i] = scanTask{
			Label: name,
			Locs:  locs,
			Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
				result, err := syncengine.ScanNWayWithProgress(ctx, locs, name, fset, progress, mode)
				if err != nil {
					return syncengine.ScanResult{}, err
				}
				results[i] = result
				if resolver != nil {
					resolver.results[i] = result
				}
				return syncengine.NWayDisplayScanResult(result), nil
			},
			// Start is deliberately nil: this session's Sync is replaced by
			// onNWaySync below, which builds and runs the real per-pair
			// transfer plan in a fresh session.
		}
	}

	syncingTitle := "Full Syncing"
	if mode == syncengine.NWayQuickScan {
		syncingTitle = "Quick Syncing"
	}

	var extras syncFlowExtras
	if mode == syncengine.NWayQuickScan {
		extras = syncFlowExtras{
			// Happy path: once the diff completes cleanly, jump straight
			// into the per-direction transfer-plan session so the user
			// reviews the actual source → dest split before committing.
			onScanDone: func() { runNWayTransfers(s, expNames, results, mode, false) },
			// Fallback: if some experiment's scan errored, onScanDone is
			// skipped and this screen renders normally so the user can see
			// the error — Sync still needs to work if pressed manually.
			onNWaySync:   func() { runNWayTransfers(s, expNames, results, mode, true) },
			syncingTitle: syncingTitle,
			quickScan:    true,
		}
	} else {
		onSync := func() {
			resolutions := resolver.buildResolutions()
			proceed := func() {
				applyNWayResolutions(s, expNames, resolver.results, locs, fset, resolutions, func(resolved []syncengine.NWayScanResult) {
					runNWayTransfers(s, expNames, resolved, mode, true)
				})
			}
			if resolver.hasDeletes() {
				showIrreversibleDeleteConfirm(s, proceed)
				return
			}
			proceed()
		}
		extras = syncFlowExtras{nway: resolver, onNWaySync: onSync, syncingTitle: syncingTitle}
	}

	showSyncFlowExtras(s, tasks, func() { showSyncExperiments(s) }, extras)
}

// runNWayTransfers builds the minimal transfer plan for every experiment and
// hands the resulting (source, dest, files) jobs to the existing scan/
// progress UI, one task per (experiment, direction) pair so its Experiments
// column shows exactly which files move which way. autoSync starts the copy
// immediately (used when the plan was already reviewed and confirmed, e.g.
// after Full Scan's conflict resolution) rather than stopping at "Ready to
// Sync" for the user to review the split first.
func runNWayTransfers(s *state, expNames []string, results []syncengine.NWayScanResult, mode syncengine.NWayScanMode, autoSync bool) {
	var tasks []scanTask
	for i, name := range expNames {
		pairs := syncengine.BuildNWayTransferPlan(results[i], syncengine.PreferLocalSource)
		for _, pair := range pairs {
			result := syncengine.ScanResultFromNWayTransfers(pair)
			tasks = append(tasks, scanTask{
				Label: fmt.Sprintf("%s: %s → %s", name, pair.Source.Name, pair.Dest.Name),
				Locs:  []syncengine.Location{pair.Source, pair.Dest},
				Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
					return result, nil
				},
				Start: func(ctx context.Context, expected syncengine.ScanResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
					return syncengine.StartSyncExperiments(ctx, pair.Source, pair.Dest, name, expected)
				},
			})
		}
	}
	if len(tasks) == 0 {
		dialog.ShowInformation("Nothing to sync", "Every selected location already agrees on every file (excluding any files you chose not to sync).", s.win)
		return
	}
	syncingTitle := "Full Syncing"
	if mode == syncengine.NWayQuickScan {
		syncingTitle = "Quick Syncing"
	}
	showSyncFlowExtras(s, tasks, func() { showSyncExperiments(s) },
		syncFlowExtras{autoSync: autoSync, syncingTitle: syncingTitle, quickScan: mode == syncengine.NWayQuickScan})
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
			result, err := syncengine.ScanNWay(ctx, locs, name, fset, syncengine.NWayFullScan)
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
