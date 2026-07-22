package ui

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// showPullFiles is the Pull Files flow: a researcher pulling a subset of one
// experiment's files into an arbitrary working directory (e.g. an R
// project), not a saved Location. Unlike Sync Experiments, this lets the user
// drill to any depth under experiments/ - a whole experiment, one
// deployment date, or one recorder directory. Scope selection is
// deliberately folder-only, not single-file: rclone's fs.NewFs returns
// ErrorIsFile (rooted at the parent, not the file) when pointed at a bare
// file path, which syncengine's copy/scan helpers don't special-case - so a
// single file isn't a safe scope choice here. destFolderBrowser is a natural
// fit for that constraint, since it only ever shows folders to browse into.
func showPullFiles(s *state) {
	names := locationNames(s.cfg.Locations)
	srcSelect := widget.NewSelect(names, nil)

	var srcLoc *syncengine.Location
	scopeLabel := widget.NewLabel("No source chosen yet.")
	destLabel := widget.NewLabel("No destination chosen")
	var destFolder string

	fullIdentCheck := widget.NewCheck("Use full path (keep source folders under destination)", nil)
	fullIdentCheck.SetChecked(true)

	scanBtn := widget.NewButton("Scan", nil)
	scanBtn.Importance = widget.HighImportance
	scanBtn.Disable()

	updateScanEnabled := func() {
		if srcLoc != nil && destFolder != "" {
			scanBtn.Enable()
		} else {
			scanBtn.Disable()
		}
	}

	browser := newDestFolderBrowser(s.win, false)
	browser.OnPathChanged = func(relPath string) {
		if srcLoc == nil {
			scopeLabel.SetText("No source chosen yet.")
			return
		}
		if relPath == "" {
			scopeLabel.SetText("Scope: entire experiments/ root")
		} else {
			scopeLabel.SetText("Scope: experiments/" + relPath)
		}
	}

	// checkSrcMissing pops the not-found prompt immediately if srcLoc is a
	// local location that isn't present on disk (e.g. an unplugged external
	// drive), rather than waiting for Scan to be pressed. onOK runs once
	// srcLoc is confirmed present (nil, remote, or reconnected); if the user
	// deselects instead, this clears srcSelect (which re-enters here with a
	// nil srcLoc) and does not call onOK.
	checkSrcMissing := func(onOK func()) {
		if srcLoc == nil {
			onOK()
			return
		}
		if missing := missingLocalLocations(*srcLoc); len(missing) > 0 {
			showLocationsNotFoundPrompt(s, missing, func(deselected []syncengine.Location) {
				// User chose to deselect the missing source; ClearSelected
				// fires srcSelect.OnChanged("") above, which resets srcLoc,
				// scope, and the child listing - so the screen is left in
				// the same state as a fresh visit, ready to retry.
				srcSelect.ClearSelected()
			}, onOK)
			return
		}
		onOK()
	}

	srcSelect.OnChanged = func(name string) {
		srcLoc = findLocation(s.cfg.Locations, name)
		// Switching source starts scope back at the experiments/ root
		// rather than carrying over a path browsed on the previous
		// source, which may not even exist there.
		browser.relPath = ""
		if srcLoc == nil {
			browser.SetLocations(nil)
		} else {
			browser.SetLocations([]syncengine.Location{*srcLoc})
		}
		updateScanEnabled()
		checkSrcMissing(func() {})
	}

	chooseDestBtn := widget.NewButton("Choose destination folder...", func() {
		chooseFolder(s.win, func(path string, err error) {
			if err != nil {
				dialog.ShowError(err, s.win)
				return
			}
			if path == "" {
				return
			}
			destFolder = path
			destLabel.SetText(destFolder)
			updateScanEnabled()
		})
	})

	scanBtn.OnTapped = func() {
		if srcLoc == nil || destFolder == "" {
			return
		}
		src := *srcLoc
		chosenRelPath := browser.RelPath()
		fset := s.cfg.DefaultFilter
		dest := destFolder
		fullIdent := fullIdentCheck.Checked

		label := "experiments/" + chosenRelPath
		if chosenRelPath == "" {
			label = "experiments/ (entire root)"
		}

		// checkSrcMissing is also run on every srcSelect change, so this is a
		// safety net for a drive that goes missing in between rather than
		// the primary catch.
		checkSrcMissing(func() {
			tasks := []scanTask{{
				Label: label,
				Locs:  []syncengine.Location{src},
				Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
					return syncengine.ScanPullFilesWithProgress(ctx, src, chosenRelPath, dest, fullIdent, fset, progress)
				},
				Start: func(ctx context.Context, result syncengine.ScanResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
					return syncengine.StartPullFiles(ctx, src, chosenRelPath, dest, fullIdent, result)
				},
			}}
			showSyncFlow(s, tasks, func() { showPullFiles(s) })
		})
	}
	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Pull Files", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewForm(&widget.FormItem{Text: "Source", Widget: srcSelect}),
		),
		container.NewVBox(
			widget.NewSeparator(),
			scopeLabel,
			container.NewHBox(chooseDestBtn, destLabel),
			fullIdentCheck,
			container.NewHBox(scanBtn, backBtn),
		),
		nil, nil,
		browser.CanvasObject(),
	)
	s.setContent(container.NewPadded(content))
}
