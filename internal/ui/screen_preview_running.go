package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

func showPreviewRunning(s *state, tasks []previewTask, onBack func()) {
	ctx, cancel := context.WithCancel(context.Background())

	title := widget.NewLabelWithStyle("Previewing", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	statusLabel := widget.NewLabel("Starting dry run...")
	currentDirLabel := widget.NewLabel("Folder: .")
	currentDirLabel.Truncation = fyne.TextTruncateEllipsis
	currentPathLabel := widget.NewLabel("File: waiting for first file")
	currentPathLabel.Truncation = fyne.TextTruncateEllipsis

	filesValue := widget.NewLabelWithStyle("0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	dirsValue := widget.NewLabelWithStyle("0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	copyValue := widget.NewLabelWithStyle("0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	sameValue := widget.NewLabelWithStyle("0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	bytesValue := widget.NewLabelWithStyle("0 B", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	bar := widget.NewProgressBarInfinite()
	bar.Start()

	var dirRows []syncengine.PreviewDirProgress
	dirList := widget.NewList(
		func() int { return len(dirRows) },
		func() fyne.CanvasObject {
			dot := canvas.NewCircle(color.NRGBA{R: 150, G: 154, B: 160, A: 255})
			name := widget.NewLabel("")
			name.Truncation = fyne.TextTruncateEllipsis
			counts := widget.NewLabel("")
			return container.NewBorder(nil, nil, container.NewGridWrap(fyne.NewSize(10, 10), dot), counts, name)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			row := dirRows[id]
			box := obj.(*fyne.Container)
			dotWrap := box.Objects[1].(*fyne.Container)
			dot := dotWrap.Objects[0].(*canvas.Circle)
			name := box.Objects[0].(*widget.Label)
			counts := box.Objects[2].(*widget.Label)

			dot.FillColor = dirColor(row)
			dot.Refresh()
			name.SetText(row.Path)
			counts.SetText(fmt.Sprintf("%d copy / %d same", row.CopyCount, row.SkipCount))
		},
	)

	var recentRows []syncengine.PreviewEntry
	recentList := widget.NewList(
		func() int { return len(recentRows) },
		func() fyne.CanvasObject {
			action := widget.NewLabel("")
			name := widget.NewLabel("")
			name.Truncation = fyne.TextTruncateEllipsis
			size := widget.NewLabel("")
			return container.NewBorder(nil, nil, action, size, name)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			entry := recentRows[len(recentRows)-1-id]
			box := obj.(*fyne.Container)
			action := box.Objects[1].(*widget.Label)
			name := box.Objects[0].(*widget.Label)
			size := box.Objects[2].(*widget.Label)
			action.SetText(previewActionLabel(entry.Action))
			name.SetText(entry.RelPath)
			size.SetText(humanBytes(entry.Size))
		},
	)

	cancelBtn := widget.NewButton("Cancel", func() {
		cancel()
		statusLabel.SetText("Canceling dry run...")
	})
	backBtn := widget.NewButton("Back", func() {
		cancel()
		onBack()
	})
	backBtn.Disable()

	metrics := container.NewGridWithColumns(5,
		metricPanel("Scanned", filesValue, color.NRGBA{R: 232, G: 240, B: 254, A: 255}),
		metricPanel("Folders", dirsValue, color.NRGBA{R: 229, G: 245, B: 236, A: 255}),
		metricPanel("To copy", copyValue, color.NRGBA{R: 255, G: 239, B: 219, A: 255}),
		metricPanel("Identical", sameValue, color.NRGBA{R: 232, G: 245, B: 233, A: 255}),
		metricPanel("Bytes", bytesValue, color.NRGBA{R: 243, G: 232, B: 255, A: 255}),
	)

	lists := container.NewHSplit(
		container.NewBorder(widget.NewLabelWithStyle("Folders being checked", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), nil, nil, nil, dirList),
		container.NewBorder(widget.NewLabelWithStyle("Recent files", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), nil, nil, nil, recentList),
	)
	lists.Offset = 0.52

	content := container.NewBorder(
		container.NewVBox(title, statusLabel, bar, metrics, currentDirLabel, currentPathLabel, widget.NewSeparator()),
		container.NewHBox(cancelBtn, backBtn),
		nil, nil,
		lists,
	)
	s.setContent(container.NewPadded(content))

	updateProgress := func(jobIndex int, p syncengine.PreviewProgress) {
		statusLabel.SetText(fmt.Sprintf("(%d/%d) %s", jobIndex+1, len(tasks), p.Label))
		filesValue.SetText(fmt.Sprintf("%d", p.FilesScanned))
		dirsValue.SetText(fmt.Sprintf("%d", p.DirsSeen))
		copyValue.SetText(fmt.Sprintf("%d", p.CopyCount))
		sameValue.SetText(fmt.Sprintf("%d", p.SkipCount))
		bytesValue.SetText(humanBytes(p.TotalBytes))
		if p.CurrentDir != "" {
			currentDirLabel.SetText("Folder: " + p.CurrentDir)
		}
		if p.CurrentPath != "" {
			currentPathLabel.SetText("File: " + p.CurrentPath)
		}
		dirRows = p.Dirs
		recentRows = p.Recent
		dirList.Refresh()
		recentList.Refresh()
	}

	go func() {
		var jobs []previewJob
		for i, task := range tasks {
			i, task := i, task
			fyne.Do(func() {
				statusLabel.SetText(fmt.Sprintf("(%d/%d) %s", i+1, len(tasks), task.Label))
			})

			result, err := task.Preview(ctx, func(p syncengine.PreviewProgress) {
				fyne.Do(func() { updateProgress(i, p) })
			})
			if err != nil {
				fyne.Do(func() {
					bar.Stop()
					cancelBtn.Disable()
					backBtn.Enable()
					if errors.Is(ctx.Err(), context.Canceled) {
						statusLabel.SetText("Dry run canceled.")
						return
					}
					statusLabel.SetText("Dry run failed: " + errString(err))
					if isAuthError(err) {
						showLocationError(s, err, task.Locs...)
					}
				})
				return
			}

			resultCopy := result
			taskCopy := task
			jobs = append(jobs, previewJob{
				Label:  taskCopy.Label,
				Result: resultCopy,
				Locs:   taskCopy.Locs,
				Start: func(ctx context.Context) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
					return taskCopy.Start(ctx, resultCopy)
				},
			})
		}

		fyne.Do(func() {
			bar.Stop()
			showPreview(s, jobs, onBack)
		})
	}()
}

func metricPanel(label string, value *widget.Label, bg color.Color) fyne.CanvasObject {
	rect := canvas.NewRectangle(bg)
	rect.CornerRadius = 8
	caption := widget.NewLabel(label)
	return container.NewStack(rect, container.NewPadded(container.NewVBox(caption, value)))
}

func dirColor(row syncengine.PreviewDirProgress) color.Color {
	switch {
	case row.CopyCount > 0:
		return color.NRGBA{R: 230, G: 126, B: 34, A: 255}
	case row.SkipCount > 0:
		return color.NRGBA{R: 46, G: 160, B: 67, A: 255}
	default:
		return color.NRGBA{R: 150, G: 154, B: 160, A: 255}
	}
}

func previewActionLabel(action syncengine.PreviewAction) string {
	if action == syncengine.ActionSkipIdentical {
		return "same"
	}
	return "copy"
}
