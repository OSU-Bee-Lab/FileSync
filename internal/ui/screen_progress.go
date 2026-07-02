package ui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

func showProgress(s *state, jobs []previewJob, onDone func()) {
	statusLabel := widget.NewLabel("Starting...")
	bar := widget.NewProgressBar()
	fileLabel := widget.NewLabel("")
	logBox := widget.NewMultiLineEntry()
	logBox.Disable()

	var currentJob atomic.Pointer[syncengine.Job]
	cancelBtn := widget.NewButton("Cancel current", func() {
		if job := currentJob.Load(); job != nil {
			job.Cancel()
		}
	})
	doneBtn := widget.NewButton("Done", onDone)
	doneBtn.Disable()

	content := container.NewBorder(
		widget.NewLabelWithStyle("Copying", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewVBox(container.NewHBox(cancelBtn, doneBtn)),
		nil, nil,
		container.NewVBox(statusLabel, bar, fileLabel, widget.NewSeparator(), container.NewVScroll(logBox)),
	)
	s.win.SetContent(container.NewPadded(content))

	go func() {
		var summary strings.Builder
		for i, j := range jobs {
			i, j := i, j
			fyne.Do(func() { statusLabel.SetText(fmt.Sprintf("(%d/%d) %s", i+1, len(jobs), j.Label)) })

			job, progress := j.Start(context.Background())
			currentJob.Store(job)

			var final syncengine.ProgressSnapshot
			for snap := range progress {
				final = snap
				snap := snap
				fyne.Do(func() {
					if snap.BytesTotal > 0 {
						bar.SetValue(float64(snap.BytesDone) / float64(snap.BytesTotal))
					}
					fileLabel.SetText(fmt.Sprintf("%s  (%d/%d files, %s/%s)",
						snap.CurrentFile, snap.FilesDone, snap.FilesTotal, humanBytes(snap.BytesDone), humanBytes(snap.BytesTotal)))
				})
			}

			statusText := "done"
			switch final.Status {
			case syncengine.JobError:
				statusText = "ERROR: " + errString(final.Err)
			case syncengine.JobCanceled:
				statusText = "canceled"
			}
			fmt.Fprintf(&summary, "%s: %s\n", j.Label, statusText)
		}

		fyne.Do(func() {
			statusLabel.SetText("All done")
			logBox.SetText(summary.String())
			doneBtn.Enable()
			cancelBtn.Disable()
		})
	}()
}
