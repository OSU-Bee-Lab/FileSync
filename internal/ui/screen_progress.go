package ui

import (
	"context"
	"fmt"
	"image/color"
	"sort"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

type uiJobStatus int

const (
	statusWaiting uiJobStatus = iota
	statusRunning
	statusDone
	statusError
	statusCanceled
)

type jobRunState struct {
	job         previewJob
	status      uiJobStatus
	err         error
	filesDone   int
	filesTotal  int
	bytesDone   int64
	bytesTotal  int64
	currentFile string
}

func uiJobColor(status uiJobStatus) color.Color {
	switch status {
	case statusRunning:
		return color.NRGBA{R: 52, G: 152, B: 219, A: 255} // Blue
	case statusDone:
		return color.NRGBA{R: 46, G: 160, B: 67, A: 255} // Green
	case statusError:
		return color.NRGBA{R: 209, G: 73, B: 73, A: 255} // Red
	case statusCanceled:
		return color.NRGBA{R: 230, G: 126, B: 34, A: 255} // Orange
	default:
		return color.NRGBA{R: 150, G: 154, B: 160, A: 255} // Grey (Waiting)
	}
}

func showProgress(s *state, jobs []previewJob, onDone func()) {
	runStates := make([]*jobRunState, len(jobs))
	for i, j := range jobs {
		runStates[i] = &jobRunState{
			job:        j,
			status:     statusWaiting,
			filesTotal: j.Result.CopyCount,
			bytesTotal: j.Result.TotalBytes,
		}
	}

	titleLabel := widget.NewLabelWithStyle("Syncing", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	statusLabel := widget.NewLabel("Starting...")

	overallBar := widget.NewProgressBar()

	progressValue := widget.NewLabelWithStyle("0 / 0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	filesValue := widget.NewLabelWithStyle("0 / 0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	bytesValue := widget.NewLabelWithStyle("0 B / 0 B", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	errorsValue := widget.NewLabelWithStyle("0", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	progressPanel := metricPanel("Progress", progressValue, color.NRGBA{R: 232, G: 240, B: 254, A: 255})
	filesPanel := metricPanel("Files", filesValue, color.NRGBA{R: 255, G: 239, B: 219, A: 255})
	bytesPanel := metricPanel("Bytes", bytesValue, color.NRGBA{R: 243, G: 232, B: 255, A: 255})
	errorsPanel := metricPanel("Errors", errorsValue, color.NRGBA{R: 240, G: 240, B: 240, A: 255})

	metrics := container.NewGridWithColumns(4, progressPanel, filesPanel, bytesPanel, errorsPanel)

	var selectedID int = 0
	var dirRows []syncengine.PreviewDirProgress
	var fileRows []syncengine.PreviewEntry

	selectedTitle := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	selectedTitle.Truncation = fyne.TextTruncateEllipsis
	selectedSummary := widget.NewLabel("")
	selectedSummary.Truncation = fyne.TextTruncateEllipsis
	selectedErrorLabel := widget.NewLabel("")
	selectedErrorLabel.Wrapping = fyne.TextWrapWord
	selectedErrorLabel.Hide()
	selectedJobBar := widget.NewProgressBar()
	selectedCurrentFile := widget.NewLabel("")
	selectedCurrentFile.Truncation = fyne.TextTruncateEllipsis
	selectedCurrentFile.Hide()

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
			counts.SetText(fmt.Sprintf("%d sync / %d same · %s", row.CopyCount, row.SkipCount, humanBytes(row.CopyBytes)))
		},
	)

	fileList := widget.NewList(
		func() int { return len(fileRows) },
		func() fyne.CanvasObject {
			dot := canvas.NewCircle(color.NRGBA{R: 150, G: 154, B: 160, A: 255})
			action := widget.NewLabel("")
			name := widget.NewLabel("")
			name.Truncation = fyne.TextTruncateEllipsis
			size := widget.NewLabel("")
			left := container.NewHBox(container.NewGridWrap(fyne.NewSize(10, 10), dot), action)
			return container.NewBorder(nil, nil, left, size, name)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			entry := fileRows[id]
			box := obj.(*fyne.Container)
			left := box.Objects[1].(*fyne.Container)
			dotWrap := left.Objects[0].(*fyne.Container)
			dot := dotWrap.Objects[0].(*canvas.Circle)
			action := left.Objects[1].(*widget.Label)
			name := box.Objects[0].(*widget.Label)
			size := box.Objects[2].(*widget.Label)

			dot.FillColor = previewEntryColor(entry.Action)
			dot.Refresh()
			action.SetText(previewActionLabel(entry.Action))
			name.SetText(entry.RelPath)
			size.SetText(humanBytes(entry.Size))
		},
	)

	refreshSelected := func(id int) {
		selectedID = id
		if id < 0 || id >= len(runStates) {
			return
		}
		st := runStates[id]
		selectedTitle.SetText(st.job.Label)

		var statusStr string
		switch st.status {
		case statusWaiting:
			statusStr = "Waiting to sync"
		case statusRunning:
			statusStr = fmt.Sprintf("Syncing: %d/%d files · %s/%s", st.filesDone, st.filesTotal, humanBytes(st.bytesDone), humanBytes(st.bytesTotal))
		case statusDone:
			statusStr = fmt.Sprintf("Completed: %d files (%s) synced", st.filesTotal, humanBytes(st.bytesTotal))
		case statusError:
			statusStr = "Failed: " + errString(st.err)
		case statusCanceled:
			statusStr = "Canceled"
		}
		selectedSummary.SetText(statusStr)

		if st.status == statusError && st.err != nil {
			selectedErrorLabel.SetText(st.err.Error())
			selectedErrorLabel.Show()
		} else {
			selectedErrorLabel.SetText("")
			selectedErrorLabel.Hide()
		}

		if st.bytesTotal > 0 {
			selectedJobBar.SetValue(float64(st.bytesDone) / float64(st.bytesTotal))
		} else {
			selectedJobBar.SetValue(0)
		}

		if st.status == statusRunning && st.currentFile != "" {
			selectedCurrentFile.SetText("Current file: " + st.currentFile)
			selectedCurrentFile.Show()
		} else {
			selectedCurrentFile.Hide()
		}

		dirRows = previewDirs(st.job.Result)
		fileRows = append([]syncengine.PreviewEntry(nil), st.job.Result.Entries...)
		sort.SliceStable(fileRows, func(i, k int) bool {
			if fileRows[i].Action != fileRows[k].Action {
				return fileRows[i].Action == syncengine.ActionCopy
			}
			return fileRows[i].RelPath < fileRows[k].RelPath
		})

		dirList.Refresh()
		fileList.Refresh()
	}

	list := widget.NewList(
		func() int { return len(runStates) },
		func() fyne.CanvasObject {
			dot := canvas.NewCircle(color.NRGBA{R: 150, G: 154, B: 160, A: 255})
			name := widget.NewLabel("")
			name.Truncation = fyne.TextTruncateEllipsis
			statusText := widget.NewLabel("")
			return container.NewBorder(nil, nil, container.NewGridWrap(fyne.NewSize(10, 10), dot), statusText, name)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			state := runStates[id]
			box := obj.(*fyne.Container)
			dotWrap := box.Objects[1].(*fyne.Container)
			dot := dotWrap.Objects[0].(*canvas.Circle)
			name := box.Objects[0].(*widget.Label)
			statusText := box.Objects[2].(*widget.Label)

			dot.FillColor = uiJobColor(state.status)
			dot.Refresh()
			name.SetText(state.job.Label)

			var st string
			switch state.status {
			case statusWaiting:
				st = "waiting"
			case statusRunning:
				if state.bytesTotal > 0 {
					st = fmt.Sprintf("syncing (%d%%)", int(float64(state.bytesDone)*100/float64(state.bytesTotal)))
				} else {
					st = "syncing..."
				}
			case statusDone:
				st = "done"
			case statusError:
				st = "failed"
			case statusCanceled:
				st = "canceled"
			}
			statusText.SetText(st)
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(runStates) {
			refreshSelected(id)
		}
	}

	detailsHeader := container.NewVBox(
		selectedTitle,
		selectedSummary,
		selectedErrorLabel,
		selectedJobBar,
		selectedCurrentFile,
		widget.NewSeparator(),
	)

	details := container.NewVSplit(
		container.NewBorder(
			detailsHeader,
			nil, nil, nil,
			dirList,
		),
		container.NewBorder(
			widget.NewLabelWithStyle("Files", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			nil, nil, nil,
			fileList,
		),
	)
	details.Offset = 0.45

	body := container.NewHSplit(
		container.NewBorder(widget.NewLabelWithStyle("Experiments", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), nil, nil, nil, list),
		details,
	)
	body.Offset = 0.34

	var currentJob atomic.Pointer[syncengine.Job]
	cancelBtn := widget.NewButton("Cancel current", func() {
		if job := currentJob.Load(); job != nil {
			job.Cancel()
		}
	})
	doneBtn := widget.NewButton("Done", onDone)
	doneBtn.Disable()

	updateUI := func() {
		var overallDoneBytes, overallTotalBytes int64
		var overallDoneFiles, overallTotalFiles int
		var compCount, errCount int

		for _, rs := range runStates {
			overallDoneBytes += rs.bytesDone
			overallTotalBytes += rs.bytesTotal
			overallDoneFiles += rs.filesDone
			overallTotalFiles += rs.filesTotal
			if rs.status == statusDone || rs.status == statusError || rs.status == statusCanceled {
				compCount++
			}
			if rs.status == statusError {
				errCount++
			}
		}

		if compCount == len(runStates) {
			if errCount > 0 {
				titleLabel.SetText("Sync Completed with Errors")
			} else {
				titleLabel.SetText("Sync Complete")
			}
			statusLabel.SetText(fmt.Sprintf("All done. %d experiments processed with %d error(s).", len(runStates), errCount))
		} else {
			titleLabel.SetText("Syncing...")
			statusLabel.SetText(fmt.Sprintf("Syncing %d experiments · %d/%d files (%s/%s)",
				len(runStates), overallDoneFiles, overallTotalFiles, humanBytes(overallDoneBytes), humanBytes(overallTotalBytes)))
		}

		if overallTotalBytes > 0 {
			overallBar.SetValue(float64(overallDoneBytes) / float64(overallTotalBytes))
		} else {
			overallBar.SetValue(0)
		}

		progressValue.SetText(fmt.Sprintf("%d / %d", compCount, len(runStates)))
		filesValue.SetText(fmt.Sprintf("%d / %d", overallDoneFiles, overallTotalFiles))
		bytesValue.SetText(fmt.Sprintf("%s / %s", humanBytes(overallDoneBytes), humanBytes(overallTotalBytes)))
		errorsValue.SetText(fmt.Sprintf("%d", errCount))

		if stack, ok := errorsPanel.(*fyne.Container); ok && len(stack.Objects) > 0 {
			if rect, ok := stack.Objects[0].(*canvas.Rectangle); ok {
				if errCount > 0 {
					rect.FillColor = color.NRGBA{R: 253, G: 237, B: 237, A: 255} // soft red
				} else {
					rect.FillColor = color.NRGBA{R: 240, G: 240, B: 240, A: 255} // light grey
				}
				rect.Refresh()
			}
		}
	}

	content := container.NewBorder(
		container.NewVBox(titleLabel, statusLabel, overallBar, metrics, widget.NewSeparator()),
		container.NewHBox(cancelBtn, doneBtn),
		nil, nil,
		body,
	)
	s.setContent(container.NewPadded(content))

	updateUI()
	if len(jobs) > 0 {
		list.Select(0)
	}

	go func() {
		for i, j := range jobs {
			i, j := i, j
			fyne.Do(func() {
				runStates[i].status = statusRunning
				list.Refresh()
				if selectedID == i {
					refreshSelected(i)
				}
				updateUI()
			})

			job, progress := j.Start(context.Background())
			currentJob.Store(job)

			var final syncengine.ProgressSnapshot
			for snap := range progress {
				final = snap
				snap := snap
				fyne.Do(func() {
					runStates[i].filesDone = snap.FilesDone
					runStates[i].filesTotal = snap.FilesTotal
					runStates[i].bytesDone = snap.BytesDone
					runStates[i].bytesTotal = snap.BytesTotal
					runStates[i].currentFile = snap.CurrentFile

					updateUI()
					if selectedID == i {
						refreshSelected(i)
					}
					list.Refresh()
				})
			}

			fyne.Do(func() {
				statusText := statusDone
				var jobErr error
				switch final.Status {
				case syncengine.JobError:
					statusText = statusError
					jobErr = final.Err
					if isAuthError(final.Err) {
						showLocationError(s, final.Err, j.Locs...)
					}
				case syncengine.JobCanceled:
					statusText = statusCanceled
				}
				runStates[i].status = statusText
				runStates[i].err = jobErr

				updateUI()
				if selectedID == i {
					refreshSelected(i)
				}
				list.Refresh()
			})
		}

		fyne.Do(func() {
			doneBtn.Enable()
			cancelBtn.Disable()
			updateUI()
		})
	}()
}
