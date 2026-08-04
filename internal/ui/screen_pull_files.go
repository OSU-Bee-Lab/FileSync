package ui

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// showPullFiles is the Pull Files flow: a researcher pulling a subset of one
// experiment's files into an arbitrary working directory (e.g. an R
// project), not a saved Location. Unlike Sync Experiments, this lets the user
// drill to any depth under experiments/ - a whole experiment, one deployment
// date, one recorder directory, or (browser.selectFiles, browser.multiSelect)
// one or more individually tapped files, even across different folders.
// ScanPullFilesWithProgress/StartPullFiles take an explicit []string of
// selected files rather than inferring them from the browsed folder, since
// rclone's fs.NewFs returns ErrorIsFile (rooted at the parent, not the file)
// when pointed at a bare file path.
func showPullFiles(s *state) {
	names := locationNames(s.cfg.Locations)
	srcSelect := widget.NewSelect(names, nil)

	var srcLoc *syncengine.Location
	scopeLabel := widget.NewLabel("No source chosen yet.")
	destLabel := widget.NewLabel("No destination chosen")
	var destFolder string

	previewLabel := widget.NewLabel("")
	previewLabel.Truncation = fyne.TextTruncateEllipsis
	spanHintLabel := widget.NewLabel("")
	spanHintLabel.Truncation = fyne.TextTruncateEllipsis
	spanHintLabel.TextStyle = fyne.TextStyle{Italic: true}

	fullIdentCheck := widget.NewCheck("Use full ident", nil)
	fullIdentCheck.SetChecked(s.pullFilesFullIdent)

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
	browser.showFiles = true
	browser.selectFiles = true
	browser.multiSelect = true

	// updatePreview mirrors ScanPullFilesWithProgress's dstRelPath logic
	// (scan.go) so the shown path matches where files will actually land.
	// With more than one file selected (possibly from different folders,
	// since a multiSelect selection survives navigation), the preview
	// collapses to their common ancestor directory rather than listing
	// every file - spanHintLabel below fills in what that's hiding.
	updatePreview := func() {
		if srcLoc == nil || destFolder == "" {
			previewLabel.SetText("")
			spanHintLabel.SetText("")
			return
		}
		files := browser.SelectedFiles()
		dstRelPath := ""
		if fullIdentCheck.Checked {
			switch {
			case len(files) == 1:
				dstRelPath = path.Dir(files[0])
				if dstRelPath == "." {
					dstRelPath = ""
				}
			case len(files) > 1:
				dstRelPath = syncengine.CommonDir(files)
			default:
				dstRelPath = browser.RelPath()
			}
		}
		previewLabel.SetText("Output: " + filepath.Join(destFolder, dstRelPath))
		spanHintLabel.SetText(spanHint(files))
	}
	browser.OnPathChanged = func(relPath string) {
		s.pullFilesRelPath = browser.relPath
		if srcLoc == nil {
			scopeLabel.SetText("No source chosen yet.")
			return
		}
		files := browser.SelectedFiles()
		switch {
		case len(files) == 1:
			scopeLabel.SetText("Pulling: experiments/" + files[0] + " (single file)")
		case len(files) > 1:
			scopeLabel.SetText("Pulling: " + plural(len(files), "file", "files") + " selected under experiments/")
		case relPath == "":
			scopeLabel.SetText("Pulling: entire experiments/ root")
		default:
			scopeLabel.SetText("Pulling: experiments/" + relPath)
		}
		updatePreview()
	}

	fullIdentCheck.OnChanged = func(checked bool) {
		s.pullFilesFullIdent = checked
		updatePreview()
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
		s.pullFilesSourceName = name
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
		updatePreview()
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
			s.pullFilesDestFolder = destFolder
			destLabel.SetText(destFolder)
			updateScanEnabled()
			updatePreview()
		})
	})

	// Rehydrate from state so a round trip through Scan (or any other
	// screen reached from here) and back restores the previously chosen
	// source, destination, and browsed scope instead of resetting to a
	// blank screen.
	if s.pullFilesDestFolder != "" {
		destFolder = s.pullFilesDestFolder
		destLabel.SetText(destFolder)
	}
	if s.pullFilesSourceName != "" && findLocation(s.cfg.Locations, s.pullFilesSourceName) != nil {
		savedRelPath := s.pullFilesRelPath
		// srcSelect.OnChanged resets browser.relPath (and, via
		// OnPathChanged, s.pullFilesRelPath) to "" as part of a genuine
		// source switch - restore the saved scope after it runs.
		srcSelect.SetSelected(s.pullFilesSourceName)
		if savedRelPath != "" {
			browser.NavigateTo(savedRelPath)
		}
	}
	updateScanEnabled()
	updatePreview()

	scanBtn.OnTapped = func() {
		if srcLoc == nil || destFolder == "" {
			return
		}
		src := *srcLoc
		chosenRelPath := browser.RelPath()
		chosenFiles := browser.SelectedFiles()
		fset := s.cfg.DefaultFilter
		dest := destFolder
		fullIdent := fullIdentCheck.Checked

		var label string
		switch {
		case len(chosenFiles) == 1:
			label = "experiments/" + chosenFiles[0]
		case len(chosenFiles) > 1:
			label = "experiments/" + syncengine.CommonDir(chosenFiles) + " (" + plural(len(chosenFiles), "file", "files") + ")"
		case chosenRelPath == "":
			label = "experiments/ (entire root)"
		default:
			label = "experiments/" + chosenRelPath
		}

		// checkSrcMissing is also run on every srcSelect change, so this is a
		// safety net for a drive that goes missing in between rather than
		// the primary catch.
		checkSrcMissing(func() {
			tasks := []scanTask{{
				Label: label,
				Locs:  []syncengine.Location{src},
				Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
					return syncengine.ScanPullFilesWithProgress(ctx, src, chosenRelPath, chosenFiles, dest, fullIdent, fset, progress)
				},
				Start: func(ctx context.Context, result syncengine.ScanResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
					return syncengine.StartPullFiles(ctx, src, chosenRelPath, chosenFiles, dest, fullIdent, result)
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
			previewLabel,
			spanHintLabel,
			actionRow(backBtn, scanBtn),
		),
		nil, nil,
		browser.CanvasObject(),
	)
	s.setContent(container.NewPadded(content))
}

// spanHint names the subfolders (immediate children of files' common
// ancestor, per syncengine.CommonDir) a multi-file selection is drawn from,
// so the researcher can tell what previewLabel's single collapsed path is
// hiding - e.g. "a.mp3, b.mp3, c.mp3" under foo/bar/nun/ plus "d.mp3" under
// foo/bar/lorem/ preview as just foo/bar/, and this fills in "Spans 2
// folders: lorem/, nun/". Empty when there's nothing to disambiguate: fewer
// than two files, or every file sits directly in the common root.
func spanHint(files []string) string {
	if len(files) < 2 {
		return ""
	}
	root := syncengine.CommonDir(files)
	subdirs := make(map[string]bool)
	for _, f := range files {
		rel := strings.TrimPrefix(f, root)
		rel = strings.TrimPrefix(rel, "/")
		dir := path.Dir(rel)
		if dir == "." {
			dir = ""
		} else {
			dir = strings.SplitN(dir, "/", 2)[0]
		}
		subdirs[dir] = true
	}
	if len(subdirs) <= 1 {
		return ""
	}
	names := make([]string, 0, len(subdirs))
	for d := range subdirs {
		if d == "" {
			names = append(names, "(root)")
		} else {
			names = append(names, d+"/")
		}
	}
	sort.Strings(names)
	return fmt.Sprintf("Spans %s: %s", plural(len(names), "folder", "folders"), strings.Join(names, ", "))
}
