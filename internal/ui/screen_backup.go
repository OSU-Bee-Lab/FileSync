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

	var srcLoc, dstLoc *syncengine.Location

	refresh := func() {
		if srcLoc == nil {
			return
		}
		ctx := context.Background()
		exps, err := syncengine.ListExperiments(ctx, *srcLoc)
		if err != nil {
			showLocationError(s, err, *srcLoc)
			return
		}
		opts := make([]string, len(exps))
		for i, e := range exps {
			opts[i] = e.Name
		}
		checkGroup.Options = opts
		checkGroup.Selected = nil
		checkGroup.Refresh()

		msg := fmt.Sprintf("%d experiment(s) found in %s", len(exps), srcLoc.Name)
		if dstLoc != nil {
			if dexps, err := syncengine.ListExperiments(ctx, *dstLoc); err == nil {
				msg += fmt.Sprintf(" · %d already present in %s", len(dexps), dstLoc.Name)
			}
		}
		statusLabel.SetText(msg)
	}

	srcSelect.OnChanged = func(name string) { srcLoc = findLocation(s.cfg.Locations, name); refresh() }
	dstSelect.OnChanged = func(name string) { dstLoc = findLocation(s.cfg.Locations, name); refresh() }

	previewBtn := widget.NewButton("Preview", func() {
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

		tasks := make([]previewTask, 0, len(selected))
		for _, name := range selected {
			name := name
			tasks = append(tasks, previewTask{
				Label: name,
				Locs:  []syncengine.Location{src, dst},
				Preview: func(ctx context.Context, progress syncengine.PreviewProgressFunc) (syncengine.PreviewResult, error) {
					return syncengine.PreviewBackupWithProgress(ctx, src, dst, name, fset, progress)
				},
				Start: func(ctx context.Context, result syncengine.PreviewResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
					return syncengine.StartBackup(ctx, src, dst, name, fset, preserveModTime, result)
				},
			})
		}
		showPreviewRunning(s, tasks, func() { showBackup(s) })
	})
	previewBtn.Importance = widget.HighImportance
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
		container.NewHBox(previewBtn, backBtn),
		nil, nil,
		container.NewVScroll(checkGroup),
	)
	s.setContent(container.NewPadded(content))
}
