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

const (
	syncModeAllWay = "All-Way Sync"
	syncModeOneWay = "One Way Sync"
)

// showSyncExperiments is the Sync Locations screen's entry point. It
// dispatches on the last-chosen mode (state.syncOneWay), and the mode
// toggle each screen renders switches back here so choosing a mode always
// rebuilds a fresh screen for it rather than trying to mutate one screen's
// widgets in place into the other's.
func showSyncExperiments(s *state) {
	if s.syncOneWay {
		showSyncExperimentsOneWay(s)
		return
	}
	showSyncExperimentsAllWay(s)
}

// syncModeToggle renders the All-Way/One Way radio shared by both modes.
// Selecting a mode persists it to state and rebuilds the screen for it.
func syncModeToggle(s *state) *widget.RadioGroup {
	g := widget.NewRadioGroup([]string{syncModeAllWay, syncModeOneWay}, func(choice string) {
		oneWay := choice == syncModeOneWay
		if oneWay == s.syncOneWay {
			return
		}
		s.syncOneWay = oneWay
		showSyncExperiments(s)
	})
	g.Horizontal = true
	if s.syncOneWay {
		g.Selected = syncModeOneWay
	} else {
		g.Selected = syncModeAllWay
	}
	return g
}

// showSyncExperimentsAllWay is the N-way flow: always whole-experiment
// granularity. There's no From/To distinction and no designated source —
// pick two or more locations and one or more experiments, and every
// selected location converges on every file, with same-name/
// different-content disagreements always surfaced for an explicit decision
// (never guessed; see syncengine.compareObjectsN).
func showSyncExperimentsAllWay(s *state) {
	allNames := locationNames(s.cfg.Locations)
	statusLabel := widget.NewLabel("")
	loading := newLoadingBar()

	locGroup := newToggleGroup(allNames, append([]string{}, s.syncExperimentsLocationNames...))

	var quickScanBtn, fullScanBtn *widget.Button
	var expGroup *widget.CheckGroup
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

	// setExpGroup replaces expGroup wholesale rather than mutating its
	// Options/Selected fields in place. Fyne's widget.CheckGroup renderer
	// (checkGroupRenderer.updateItems, checked_group.go) has a bug: when
	// reused across an Options change, it reads a reused Check item's stale
	// Text (from the option that used to live at that index) to decide
	// Checked *before* overwriting Text with the new option — so a row can
	// render checked for whatever option happens to land at an index that
	// previously held a selected one. We rebuild the union incrementally as
	// locations report in (see refresh below), which reorders/inserts
	// options on essentially every update and reliably tripped this. A
	// brand-new CheckGroup always starts with zero items, so every Check is
	// constructed fresh with its correct name baked in — no stale index to
	// misread.
	expScroll := container.NewVScroll(widget.NewCheckGroup(nil, nil))
	setExpGroup := func(options, selected []string) {
		g := widget.NewCheckGroup(options, nil)
		g.Selected = selected
		g.OnChanged = func(sel []string) {
			s.syncExperimentsExpNames = sel
			updateScanBtn()
		}
		g.Refresh()
		expGroup = g
		expScroll.Content = g
		expScroll.Refresh()
	}
	setExpGroup(nil, nil)

	// refresh reloads the experiment list as the union of every experiment
	// visible from any of the currently-selected locations.
	var refresh func()
	refresh = func() {
		names := locGroup.Selected()
		if len(names) == 0 {
			setExpGroup(nil, nil)
			updateScanBtn()
			statusLabel.SetText("")
			return
		}
		locs := locationsFromNamesAny(s.cfg.Locations, names)

		// Catch an unplugged local drive right away, at selection time,
		// rather than letting it surface as an opaque listing error below or
		// waiting until Quick/Full Scan is pressed (startScanMode's own
		// missingLocalLocations check further down stays as a safety net for
		// a drive that goes missing between here and then).
		if missing := missingLocalLocations(locs...); len(missing) > 0 {
			showLocationsNotFoundPrompt(s, missing, func(deselected []syncengine.Location) {
				keep := make([]string, 0, len(names))
				for _, name := range names {
					if loc := findLocation(s.cfg.Locations, name); loc == nil || !containsLocation(deselected, *loc) {
						keep = append(keep, name)
					}
				}
				locGroup.SetSelected(keep)
				s.syncExperimentsLocationNames = keep
				refresh()
			}, refresh)
			return
		}

		statusLabel.SetText("")
		loading.Show()
		updateScanBtn()

		// applyUnion re-renders expGroup from the current union/seen state -
		// called both incrementally (as each location's listing lands, so a
		// fast location's experiments show right away instead of waiting on
		// a slow one) and once more at the end for the final status text.
		applyUnion := func(union []string, seen map[string]bool) {
			keep := make([]string, 0, len(s.syncExperimentsExpNames))
			for _, name := range s.syncExperimentsExpNames {
				if seen[name] {
					keep = append(keep, name)
				}
			}
			s.syncExperimentsExpNames = keep
			setExpGroup(union, keep)
			updateScanBtn()
		}

		// Seed the union with whatever's already showing (from the prior
		// selection) so adding a location grows the list in place instead of
		// blanking it while the fresh scan's goroutines report back in one
		// at a time - mirrors dest_folder_browser's union-preserving reload.
		var seedNames []string
		if expGroup != nil {
			seedNames = append(seedNames, expGroup.Options...)
		}

		go func() {
			ctx := context.Background()
			var mu sync.Mutex
			seen := map[string]bool{}
			var union []string
			for _, name := range seedNames {
				if !seen[name] {
					seen[name] = true
					union = append(union, name)
				}
			}
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
	if len(locGroup.Selected()) >= 1 {
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
			showLocationsNotFoundPrompt(s, missing, func(deselected []syncengine.Location) {
				keep := make([]string, 0, len(names))
				for _, name := range names {
					if loc := findLocation(s.cfg.Locations, name); loc == nil || !containsLocation(deselected, *loc) {
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
			widget.NewLabelWithStyle("Sync Locations", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			container.NewPadded(syncModeToggle(s)),
			widget.NewLabel("Pick two or more locations to sync."),
			container.NewPadded(locGroup.CanvasObject()),
			loading.CanvasObject(),
			statusLabel,
			widget.NewSeparator(),
		),
		container.NewHBox(quickScanBtn, fullScanBtn, backBtn),
		nil, nil,
		expScroll,
	)
	s.setContent(container.NewPadded(content))
}

// showSyncExperimentsOneWay is the directional flow: pick an arbitrary local
// folder (an OS folder picker — not necessarily anything under a Location's
// experiments root) and push its contents one way onto an existing or new
// folder within any single Location, browsed like a destination rather than
// picked from a fixed schema. This is the inverse of Pull Files: there, a
// Location is the browsed source and a raw local folder is the destination;
// here, a raw local folder is the source and a Location is the browsed
// destination. Files that exist only at the destination are left untouched
// (never deleted), same as every other copy path in this app. It exists for
// the case where simultaneously connecting to every Location isn't
// practical — e.g. pushing a batch of recordings from a laptop up to a
// remote, to be reconciled later from a machine with access to everything.
func showSyncExperimentsOneWay(s *state) {
	names := locationNames(s.cfg.Locations)
	dstSelect := widget.NewSelect(names, nil)

	var dstLoc *syncengine.Location
	srcLabel := widget.NewLabel("No source folder chosen")
	destLabel := widget.NewLabel("No destination chosen")
	srcFolder := s.syncOneWayFromFolder
	if srcFolder != "" {
		srcLabel.SetText(srcFolder)
	}

	quickScanBtn := widget.NewButton("Quick Scan", nil)
	quickScanBtn.Importance = widget.HighImportance
	quickScanBtn.Disable()
	fullScanBtn := widget.NewButton("Full Scan", nil)
	fullScanBtn.Disable()

	updateSyncEnabled := func() {
		if dstLoc != nil && srcFolder != "" {
			quickScanBtn.Enable()
			fullScanBtn.Enable()
		} else {
			quickScanBtn.Disable()
			fullScanBtn.Disable()
		}
	}

	browser := newDestFolderBrowser(s.win, true)
	browser.OnPathChanged = func(relPath string) {
		s.syncOneWayToRelPath = relPath
		if dstLoc == nil {
			destLabel.SetText("No destination chosen")
			return
		}
		if relPath == "" {
			destLabel.SetText(dstLoc.Name + ": /")
		} else {
			destLabel.SetText(dstLoc.Name + ": /" + relPath)
		}
	}

	// checkDstMissing pops the not-found prompt immediately if dstLoc is a
	// local location that isn't present on disk (e.g. an unplugged external
	// drive), mirroring Pull Files' checkSrcMissing but for the destination
	// side, since here the Location is the destination rather than source.
	checkDstMissing := func(onOK func()) {
		if dstLoc == nil {
			onOK()
			return
		}
		if missing := missingLocalLocations(*dstLoc); len(missing) > 0 {
			showLocationsNotFoundPrompt(s, missing, func(deselected []syncengine.Location) {
				dstSelect.ClearSelected()
			}, onOK)
			return
		}
		onOK()
	}

	dstSelect.OnChanged = func(name string) {
		dstLoc = findLocation(s.cfg.Locations, name)
		s.syncOneWayToName = name
		browser.relPath = ""
		if dstLoc == nil {
			browser.SetLocations(nil)
			destLabel.SetText("No destination chosen")
		} else {
			browser.SetLocations([]syncengine.Location{*dstLoc})
		}
		updateSyncEnabled()
		checkDstMissing(func() {})
	}
	if contains(names, s.syncOneWayToName) {
		dstSelect.SetSelected(s.syncOneWayToName) // fires OnChanged above
		browser.relPath = s.syncOneWayToRelPath
		browser.reload()
	}

	chooseSrcBtn := widget.NewButton("Choose source folder...", func() {
		chooseFolder(s.win, func(path string, err error) {
			if err != nil {
				dialog.ShowError(err, s.win)
				return
			}
			if path == "" {
				return
			}
			srcFolder = path
			s.syncOneWayFromFolder = path
			srcLabel.SetText(srcFolder)
			updateSyncEnabled()
		})
	})

	startScan := func(mode syncengine.NWayScanMode) {
		if dstLoc == nil || srcFolder == "" {
			return
		}
		dst := *dstLoc
		dstRelPath := browser.RelPath()

		label := "→ " + dst.Name
		if dstRelPath != "" {
			label += "/" + dstRelPath
		}

		// checkDstMissing is also run on every dstSelect change, so this is a
		// safety net for a drive that goes missing in between rather than
		// the primary catch.
		checkDstMissing(func() {
			src := syncengine.LocalFolderLocation("Source folder", srcFolder)
			dstSub := syncengine.SubLocation(dst, dstRelPath)
			runOneWayScan(s, src, dstSub, label, mode)
		})
	}
	quickScanBtn.OnTapped = func() { startScan(syncengine.NWayQuickScan) }
	fullScanBtn.OnTapped = func() { startScan(syncengine.NWayFullScan) }
	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Sync Locations", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			container.NewPadded(syncModeToggle(s)),
			widget.NewLabel("Push a local folder's contents one-way onto a folder in any location."),
			container.NewHBox(chooseSrcBtn, srcLabel),
			widget.NewForm(&widget.FormItem{Text: "To location", Widget: dstSelect}),
		),
		container.NewVBox(
			widget.NewSeparator(),
			destLabel,
			container.NewHBox(quickScanBtn, fullScanBtn, backBtn),
		),
		nil, nil,
		browser.CanvasObject(),
	)
	s.setContent(container.NewPadded(content))
}

// oneWayPseudoName is the internal "experiment name" runOneWayScan/
// runOneWayTransfers thread through the N-way engine as the scan relPath.
// It's always "" — src and dst are ephemeral Locations (see
// syncengine.LocalFolderLocation/SubLocation) whose own RootPath already
// points at the exact folders to compare, so no further relPath is needed
// on top of that. It only exists as a slice element because the N-way
// resolver/applyNWayResolutions machinery is keyed by a list of names.
const oneWayPseudoName = ""

// runOneWayScan scans a source folder against a destination folder through
// the exact same N-way conflict-resolution machinery as All-Way Sync (see
// runNWayScan) — same resolver, same dialog, same Overwrite/Rename/Delete
// options — but restricted to these two ephemeral Locations. Once every
// conflict is resolved, runOneWayTransfers keeps only the source→destination
// leg of the resulting transfer plan, discarding any destination→source leg,
// so this can never write back into the local source folder no matter how a
// conflict is resolved (see runOneWayTransfers).
//
// mode mirrors All-Way exactly: NWayFullScan reads bytes and gates the sync
// behind per-file conflict resolution; NWayQuickScan checks presence only,
// can never produce a conflict, so no resolver is built and pressing sync
// goes straight to the transfer preview (see runNWayScan).
func runOneWayScan(s *state, src, dst syncengine.Location, label string, mode syncengine.NWayScanMode) {
	fset := s.cfg.DefaultFilter
	locs := []syncengine.Location{src, dst}
	names := []string{oneWayPseudoName}

	var resolver *nwayResolver
	if mode == syncengine.NWayFullScan {
		resolver = newNWayResolver(names)
	}
	results := make([]syncengine.NWayScanResult, 1)

	tasks := []scanTask{{
		Label: label,
		Locs:  locs,
		Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
			result, err := syncengine.ScanNWayWithProgress(ctx, locs, oneWayPseudoName, fset, progress, mode)
			if err != nil {
				return syncengine.ScanResult{}, err
			}
			results[0] = result
			if resolver != nil {
				resolver.results[0] = result
			}
			return syncengine.NWayDisplayScanResult(result), nil
		},
		// Start is deliberately nil, same as runNWayScan: Sync is replaced by
		// onNWaySync below, which builds and runs the real transfer plan
		// (filtered to the forward leg only) in a fresh session.
	}}

	syncingTitle := "Full Syncing"
	if mode == syncengine.NWayQuickScan {
		syncingTitle = "Quick Syncing"
	}

	var extras syncFlowExtras
	if mode == syncengine.NWayQuickScan {
		extras = syncFlowExtras{
			// Happy path: once the diff completes cleanly, jump straight into
			// the forward-leg transfer preview so the user reviews the actual
			// source → dest split before committing.
			onScanDone: func() { runOneWayTransfers(s, src, dst, label, results[0], mode) },
			// Fallback: if the scan errored, onScanDone is skipped and this
			// screen renders normally so the user can see the error — Sync
			// still needs to work if pressed manually.
			onNWaySync:   func() { runOneWayTransfers(s, src, dst, label, results[0], mode) },
			syncingTitle: syncingTitle,
			quickScan:    true,
		}
	} else {
		onSync := func() {
			resolutions := resolver.buildResolutions()
			proceed := func() {
				applyNWayResolutions(s, names, resolver.results, locs, fset, resolutions, func(resolved []syncengine.NWayScanResult) {
					runOneWayTransfers(s, src, dst, label, resolved[0], mode)
				})
			}
			if resolver.hasDeletes() {
				showIrreversibleDeleteConfirm(s,
					"This will permanently delete the selected file(s) from the chosen location(s). This cannot be undone.",
					"Delete and Sync", proceed)
				return
			}
			proceed()
		}
		extras = syncFlowExtras{nway: resolver, onNWaySync: onSync, syncingTitle: syncingTitle}
	}

	showSyncFlowExtras(s, tasks, func() { showSyncExperiments(s) }, extras)
}

// runOneWayTransfers builds the minimal transfer plan the same way
// runNWayTransfers does, but keeps only the source→destination leg. A
// conflict resolved as "destination wins" turns into a transfer plan entry
// sourced from dst, which would otherwise propagate back into the local
// source folder — dropping any such entry here means that choice simply
// skips the file (never touches the source folder) instead, keeping the
// push strictly one-way regardless of how a conflict was resolved.
func runOneWayTransfers(s *state, src, dst syncengine.Location, label string, result syncengine.NWayScanResult, mode syncengine.NWayScanMode) {
	pairs := syncengine.BuildNWayTransferPlan(result, syncengine.PreferLocalSource)
	var tasks []scanTask
	for _, pair := range pairs {
		if pair.Source.ID != src.ID || pair.Dest.ID != dst.ID {
			continue
		}
		transferResult := syncengine.ScanResultFromNWayTransfers(pair)
		tasks = append(tasks, scanTask{
			Label: label,
			Locs:  []syncengine.Location{pair.Source, pair.Dest},
			Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
				return transferResult, nil
			},
			Start: func(ctx context.Context, expected syncengine.ScanResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
				return syncengine.StartSyncExperiments(ctx, pair.Source, pair.Dest, oneWayPseudoName, expected)
			},
		})
	}
	syncingTitle := "Full Syncing"
	if mode == syncengine.NWayQuickScan {
		syncingTitle = "Quick Syncing"
	}
	if len(tasks) == 0 {
		msg := "Every file in the source folder already exists at the destination."
		if mode == syncengine.NWayQuickScan {
			msg += " This quick sync can determine if the files are present, but not if the files are identical. Run a Full Scan to check file contents."
		}
		showSyncFlowExtras(s, nil, func() { showSyncExperiments(s) }, syncFlowExtras{
			finishedTitle:   "Already in sync!",
			finishedMessage: msg,
		})
		return
	}
	showSyncFlowExtras(s, tasks, func() { showSyncExperiments(s) },
		syncFlowExtras{autoSync: true, syncingTitle: syncingTitle, quickScan: mode == syncengine.NWayQuickScan})
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
				showIrreversibleDeleteConfirm(s,
					"This will permanently delete the selected file(s) from the chosen location(s). This cannot be undone.",
					"Delete and Sync", proceed)
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
		msg := "All files in these locations already exist in the selected locations."
		if mode == syncengine.NWayQuickScan {
			msg += " This quick sync can determine if the files are present, but not if the files are identical. Run a Full Sync to check file contents."
		}
		// Render the normal finished-sync chrome (see showSyncFlowExtras'
		// zero-task path) rather than a blocking dialog. This is reached
		// from onScanDone while the prior scan screen is still mid-scan
		// (Back disabled, no further phase transition coming for it) — a
		// plain dialog would leave that screen stuck showing "Scanning...";
		// setContent here replaces it outright.
		showSyncFlowExtras(s, nil, func() { showSyncExperiments(s) }, syncFlowExtras{
			finishedTitle:   "Already in sync!",
			finishedMessage: msg,
		})
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

// contains reports whether name appears in names.
func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
