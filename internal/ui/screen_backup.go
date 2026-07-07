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

// showBackup is the Sync flow: always whole-experiment granularity,
// always Location <-> Location. It never exposes sub-experiment
// drilling - that's the Download flow's job (screen_download.go).
func showBackup(s *state) {
	names := locationNames(s.cfg.Locations)
	srcSelect := widget.NewSelect(names, nil)
	dstSelect := widget.NewSelect(names, nil)
	statusLabel := widget.NewLabel("Pick a from and to location.")

	checkGroup := widget.NewCheckGroup(nil, nil)

	var scanBtn *widget.Button
	updateScanBtn := func() {
		if scanBtn == nil {
			return
		}
		if len(checkGroup.Selected) == 0 {
			scanBtn.Disable()
		} else {
			scanBtn.Enable()
		}
	}
	checkGroup.OnChanged = func(_ []string) { updateScanBtn() }

	var srcLoc, dstLoc *syncengine.Location
	if loc := findLocation(s.cfg.Locations, s.backupSrcName); loc != nil {
		srcLoc = loc
		srcSelect.Selected = loc.Name
	}
	if loc := findLocation(s.cfg.Locations, s.backupDstName); loc != nil {
		dstLoc = loc
		dstSelect.Selected = loc.Name
	}

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
		s.backupSrcName = name
		if dstLoc != nil && srcLoc != nil && dstLoc.ID == srcLoc.ID {
			dstLoc = nil
			s.backupDstName = ""
			dstSelect.ClearSelected()
		}
		refresh()
	}
	dstSelect.OnChanged = func(name string) {
		dstLoc = findLocation(s.cfg.Locations, name)
		s.backupDstName = name
		if srcLoc != nil && dstLoc != nil && srcLoc.ID == dstLoc.ID {
			srcLoc = nil
			s.backupSrcName = ""
			srcSelect.ClearSelected()
		}
		refresh()
	}

	if srcLoc != nil {
		refresh()
	}

	scanBtn = widget.NewButton("Scan", func() {
		if srcLoc == nil || dstLoc == nil {
			dialog.ShowInformation("Pick locations", "Choose a from and to location first.", s.win)
			return
		}
		if srcLoc.ID == dstLoc.ID {
			dialog.ShowInformation("Same location", "From and to must be different locations.", s.win)
			return
		}
		selected := append([]string{}, checkGroup.Selected...)
		if len(selected) == 0 {
			dialog.ShowInformation("Pick experiments", "Select at least one experiment to back up.", s.win)
			return
		}
		src, dst := *srcLoc, *dstLoc
		fset, preserveModTime := s.cfg.DefaultFilter, s.cfg.PreserveModTime

		tasks := make([]scanTask, 0, len(selected))
		for _, name := range selected {
			name := name
			tasks = append(tasks, scanTask{
				Label: name,
				Locs:  []syncengine.Location{src, dst},
				Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
					return syncengine.ScanBackupWithProgress(ctx, src, dst, name, fset, progress)
				},
				Start: func(ctx context.Context, result syncengine.ScanResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
					return syncengine.StartBackup(ctx, src, dst, name, fset, preserveModTime, result)
				},
			})
		}
		showScanRunning(s, tasks, func() { showBackup(s) })
	})
	scanBtn.Importance = widget.HighImportance
	updateScanBtn()
	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Sync", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewForm(
				&widget.FormItem{Text: "From", Widget: srcSelect},
				&widget.FormItem{Text: "To", Widget: dstSelect},
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
