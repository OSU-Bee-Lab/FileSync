package ui

import (
	"context"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

// showSyncExperiments is the Sync Experiments flow: always whole-experiment
// granularity, always Location <-> Location(s). It never exposes
// sub-experiment drilling - that's the Download flow's job
// (screen_download.go). Exactly one "From" location is picked, and one or
// more "To" locations - local and cloud are offered identically, since
// which is actually reachable is checked live at scan time (see
// missingLocalLocations), not by how the picker is built.
func showSyncExperiments(s *state) {
	allNames := locationNames(s.cfg.Locations)
	srcSelect := widget.NewSelect(allNames, nil)
	statusLabel := widget.NewLabel("Pick a from location and at least one to location.")

	checkGroup := widget.NewCheckGroup(nil, nil)

	var dstGroup *toggleGroup
	dstBox := container.NewVBox()

	var scanBtn *widget.Button
	updateScanBtn := func() {
		if scanBtn == nil {
			return
		}
		if len(checkGroup.Selected) > 0 && dstGroup != nil && len(dstGroup.Selected()) > 0 {
			scanBtn.Enable()
		} else {
			scanBtn.Disable()
		}
	}
	checkGroup.OnChanged = func(_ []string) { updateScanBtn() }

	var srcLoc *syncengine.Location

	// rebuildDstGroup offers every Location except the current "From" as a
	// "To" destination, preserving whichever of the previous selection is
	// still offered (a location can't be picked as both From and To at
	// once, so it's simply excluded from To's options rather than
	// cross-checked at scan time).
	rebuildDstGroup := func() {
		var keep []string
		if dstGroup != nil {
			keep = dstGroup.Selected()
		} else {
			keep = append([]string{}, s.syncExperimentsDstNames...)
		}
		var opts []string
		for _, name := range allNames {
			if srcLoc != nil && name == srcLoc.Name {
				continue
			}
			opts = append(opts, name)
		}
		var preselected []string
		for _, name := range keep {
			if name != "" && (srcLoc == nil || name != srcLoc.Name) {
				for _, o := range opts {
					if o == name {
						preselected = append(preselected, name)
						break
					}
				}
			}
		}
		dstGroup = newToggleGroup(opts, preselected)
		dstGroup.OnChanged = func(selected []string) {
			s.syncExperimentsDstNames = selected
			updateScanBtn()
		}
		dstBox.Objects = []fyne.CanvasObject{dstGroup.CanvasObject()}
		dstBox.Refresh()
		s.syncExperimentsDstNames = dstGroup.Selected()
	}

	if loc := findLocation(s.cfg.Locations, s.syncExperimentsSrcName); loc != nil {
		srcLoc = loc
		srcSelect.Selected = loc.Name
	}
	rebuildDstGroup()

	refresh := func() {
		if srcLoc == nil {
			return
		}
		statusLabel.SetText("Loading experiments...")
		checkGroup.Options = nil
		checkGroup.Selected = nil
		checkGroup.Refresh()
		updateScanBtn()

		src := *srcLoc

		go func() {
			ctx := context.Background()
			exps, err := syncengine.ListExperiments(ctx, src)

			fyne.Do(func() {
				if srcLoc == nil || srcLoc.ID != src.ID {
					return
				}

				if err != nil {
					showLocationError(s, err, src)
					statusLabel.SetText("Error loading experiments.")
					return
				}

				opts := make([]string, len(exps))
				for i, e := range exps {
					opts[i] = e.Name
				}
				checkGroup.Options = opts
				checkGroup.Selected = nil
				checkGroup.Refresh()
				updateScanBtn()

				statusLabel.SetText(fmt.Sprintf("%d experiment(s) found in %s", len(exps), src.Name))
			})
		}()
	}

	srcSelect.OnChanged = func(name string) {
		srcLoc = findLocation(s.cfg.Locations, name)
		s.syncExperimentsSrcName = name
		rebuildDstGroup()
		refresh()
	}

	if srcLoc != nil {
		refresh()
	}

	scanBtn = widget.NewButton("Scan", func() {
		dstNames := dstGroup.Selected()
		if srcLoc == nil || len(dstNames) == 0 {
			dialog.ShowInformation("Pick locations", "Choose a from location and at least one to location first.", s.win)
			return
		}
		selected := append([]string{}, checkGroup.Selected...)
		if len(selected) == 0 {
			dialog.ShowInformation("Pick experiments", "Select at least one experiment to sync.", s.win)
			return
		}
		src := *srcLoc
		dsts := locationsFromNamesAny(s.cfg.Locations, dstNames)

		startScan := func() {
			fset, preserveModTime := s.cfg.DefaultFilter, s.cfg.PreserveModTime

			tasks := make([]scanTask, 0, len(selected)*len(dsts))
			for _, name := range selected {
				name := name
				for _, dst := range dsts {
					dst := dst
					label := name
					if len(dsts) > 1 {
						label = fmt.Sprintf("%s → %s", name, dst.Name)
					}
					tasks = append(tasks, scanTask{
						Label: label,
						Locs:  []syncengine.Location{src, dst},
						Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
							return syncengine.ScanSyncExperimentsWithProgress(ctx, src, dst, name, fset, progress)
						},
						Start: func(ctx context.Context, result syncengine.ScanResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
							return syncengine.StartSyncExperiments(ctx, src, dst, name, fset, preserveModTime, result)
						},
					})
				}
			}
			showScanRunning(s, tasks, func() { showSyncExperiments(s) })
		}

		if missing := missingLocalLocations(append([]syncengine.Location{src}, dsts...)...); len(missing) > 0 {
			showLocationsNotFoundPrompt(s, missing, func() {
				if containsLocation(missing, src) {
					srcLoc = nil
					s.syncExperimentsSrcName = ""
					srcSelect.ClearSelected()
					rebuildDstGroup()
					return
				}
				keep := make([]string, 0, len(dstNames))
				for _, name := range dstNames {
					if loc := findLocation(s.cfg.Locations, name); loc == nil || !containsLocation(missing, *loc) {
						keep = append(keep, name)
					}
				}
				dstGroup.SetSelected(keep)
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
			widget.NewForm(
				&widget.FormItem{Text: "From", Widget: srcSelect},
				&widget.FormItem{Text: "To", Widget: dstBox},
			),
			statusLabel,
			widget.NewSeparator(),
		),
		container.NewHBox(scanBtn, backBtn),
		nil, nil,
		container.NewVScroll(checkGroup),
	)
	s.setContent(container.NewPadded(content))
}
