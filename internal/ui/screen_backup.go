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

// showBackup is the Backup/Sync flow: always whole-experiment granularity,
// always Location <-> Location. It never exposes sub-experiment
// drilling - that's the Download flow's job (screen_download.go).
func showBackup(s *state) {
	names := locationNames(s.cfg.Locations)
	srcSelect := widget.NewSelect(names, nil)
	dstSelect := widget.NewSelect(names, nil)
	statusLabel := widget.NewLabel("Pick a source and destination location.")

	checkGroup := widget.NewCheckGroup(nil, nil)

	var srcLoc, dstLoc *syncengine.Location

	refresh := func() {
		if srcLoc == nil {
			return
		}
		ctx := context.Background()
		exps, err := syncengine.ListExperiments(ctx, *srcLoc)
		if err != nil {
			dialog.ShowError(err, s.win)
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
			dialog.ShowInformation("Pick locations", "Choose a source and destination location first.", s.win)
			return
		}
		if srcLoc.ID == dstLoc.ID {
			dialog.ShowInformation("Same location", "Source and destination must be different locations.", s.win)
			return
		}
		selected := append([]string{}, checkGroup.Selected...)
		if len(selected) == 0 {
			dialog.ShowInformation("Pick experiments", "Select at least one experiment to back up.", s.win)
			return
		}
		src, dst := *srcLoc, *dstLoc
		fset, preserveModTime := s.cfg.DefaultFilter, s.cfg.PreserveModTime

		progressDialog := dialog.NewCustom("Checking...", "Please wait", widget.NewLabel("Running dry run for "+fmt.Sprint(len(selected))+" experiment(s)..."), s.win)
		progressDialog.Show()

		go func() {
			ctx := context.Background()
			var jobs []previewJob
			for _, name := range selected {
				name := name
				result, err := syncengine.PreviewBackup(ctx, src, dst, name, fset)
				if err != nil {
					fyne.Do(func() {
						progressDialog.Hide()
						dialog.ShowError(err, s.win)
					})
					return
				}
				jobs = append(jobs, previewJob{
					Label:  name,
					Result: result,
					Start: func(ctx context.Context) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
						return syncengine.StartBackup(ctx, src, dst, name, fset, preserveModTime, result)
					},
				})
			}
			fyne.Do(func() {
				progressDialog.Hide()
				showPreview(s, jobs, func() { showBackup(s) })
			})
		}()
	})
	previewBtn.Importance = widget.HighImportance
	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Backup / Sync", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewForm(
				&widget.FormItem{Text: "Source", Widget: srcSelect},
				&widget.FormItem{Text: "Destination", Widget: dstSelect},
			),
			statusLabel,
			widget.NewSeparator(),
		),
		container.NewHBox(previewBtn, backBtn),
		nil, nil,
		container.NewVScroll(checkGroup),
	)
	s.win.SetContent(container.NewPadded(content))
}
