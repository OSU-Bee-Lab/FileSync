package ui

import (
	"context"
	"fmt"
	"path"

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
// file path, which syncengine's copy/scan helpers don't special-case -
// so a single file isn't a safe scope choice here.
func showPullFiles(s *state) {
	names := locationNames(s.cfg.Locations)
	srcSelect := widget.NewSelect(names, nil)

	var srcLoc *syncengine.Location
	relPath := ""
	var scopePath string // "" until the user picks a scope folder to pull

	breadcrumb := widget.NewLabel("experiments/")
	scopeLabel := widget.NewLabel("No scope chosen yet - tap \"Use this\" on a folder or file below.")
	destLabel := widget.NewLabel("No destination chosen")
	var destFolder string

	var entries []syncengine.Entry
	list := widget.NewList(
		func() int { return len(entries) },
		func() fyne.CanvasObject {
			return container.NewBorder(nil, nil, nil, widget.NewButton("Use this", nil), widget.NewButton("", nil))
		},
		nil,
	)

	upBtn := widget.NewButton("Up", nil)

	var loadChildren func()
	loadChildren = func() {
		if srcLoc == nil {
			return
		}
		if relPath == "" {
			breadcrumb.SetText("Loading experiments/...")
		} else {
			breadcrumb.SetText("Loading experiments/" + relPath + "/...")
		}
		entries = nil
		list.Refresh()
		upBtn.Disable()

		src := *srcLoc
		path := relPath

		go func() {
			ctx := context.Background()
			result, err := syncengine.ListChildren(ctx, src, path)

			fyne.Do(func() {
				if srcLoc == nil || srcLoc.ID != src.ID || relPath != path {
					return
				}
				if err != nil {
					if relPath == "" {
						breadcrumb.SetText("Error loading experiments/")
					} else {
						breadcrumb.SetText("Error loading experiments/" + relPath + "/")
					}
					showLocationError(s, err, src)
					return
				}
				entries = result
				if relPath == "" {
					breadcrumb.SetText("experiments/")
				} else {
					breadcrumb.SetText("experiments/" + relPath + "/")
				}
				upBtn.Disable()
				if relPath != "" {
					upBtn.Enable()
				}
				list.Refresh()
			})
		}()
	}

	list.UpdateItem = func(id widget.ListItemID, obj fyne.CanvasObject) {
		e := entries[id]
		border := obj.(*fyne.Container)
		openBtn := border.Objects[1].(*widget.Button)
		useBtn := border.Objects[0].(*widget.Button)

		label := e.Name
		if e.IsDir {
			label = "\U0001F4C1 " + label // folder icon
		} else {
			label = fmt.Sprintf("%s  (%s)", label, humanBytes(e.Size))
		}
		openBtn.SetText(label)
		openBtn.OnTapped = func() {
			if e.IsDir {
				relPath = joinRel(relPath, e.Name)
				loadChildren()
			}
		}

		if e.IsDir {
			useBtn.Enable()
			useBtn.OnTapped = func() {
				scopePath = joinRel(relPath, e.Name)
				scopeLabel.SetText("Scope: experiments/" + scopePath)
			}
		} else {
			useBtn.OnTapped = nil
			useBtn.Disable()
		}
	}

	upBtn.OnTapped = func() {
		relPath = path.Dir(relPath)
		if relPath == "." {
			relPath = ""
		}
		loadChildren()
	}

	useCurrentFolderBtn := widget.NewButton("Use current folder as scope", func() {
		scopePath = relPath
		if scopePath == "" {
			scopeLabel.SetText("Scope: entire experiments/ root")
		} else {
			scopeLabel.SetText("Scope: experiments/" + scopePath)
		}
	})

	srcSelect.OnChanged = func(name string) {
		srcLoc = findLocation(s.cfg.Locations, name)
		relPath = ""
		scopePath = ""
		scopeLabel.SetText("No scope chosen yet - tap \"Use this\" on a folder or file below.")
		loadChildren()
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
		})
	})

	scanBtn := widget.NewButton("Scan", func() {
		if srcLoc == nil {
			dialog.ShowInformation("Pick a source", "Choose a source location first.", s.win)
			return
		}
		if destFolder == "" {
			dialog.ShowInformation("Pick a destination", "Choose a destination folder first.", s.win)
			return
		}
		src := *srcLoc
		chosenRelPath := scopePath
		fset, preserveModTime := s.cfg.DefaultFilter, s.cfg.PreserveModTime
		dest := destFolder

		label := "experiments/" + chosenRelPath
		if chosenRelPath == "" {
			label = "experiments/ (entire root)"
		}
		tasks := []scanTask{{
			Label: label,
			Locs:  []syncengine.Location{src},
			Scan: func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error) {
				return syncengine.ScanPullFilesWithProgress(ctx, src, chosenRelPath, dest, fset, progress)
			},
			Start: func(ctx context.Context, result syncengine.ScanResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
				return syncengine.StartPullFiles(ctx, src, chosenRelPath, dest, fset, preserveModTime, result)
			},
		}}
		showScanRunning(s, tasks, func() { showPullFiles(s) })
	})
	scanBtn.Importance = widget.HighImportance
	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Pull Files", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewForm(&widget.FormItem{Text: "Source", Widget: srcSelect}),
			container.NewHBox(upBtn, breadcrumb),
		),
		container.NewVBox(
			widget.NewSeparator(),
			scopeLabel,
			useCurrentFolderBtn,
			container.NewHBox(chooseDestBtn, destLabel),
			container.NewHBox(scanBtn, backBtn),
		),
		nil, nil,
		list,
	)
	s.setContent(container.NewPadded(content))
	upBtn.Disable()
}
