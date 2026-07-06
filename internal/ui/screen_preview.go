package ui

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

// previewJob is one dry-run result the user is being asked to confirm,
// paired with the closure that actually runs the real copy for it. It's
// deliberately generic over Backup vs Download - screen_preview.go and
// screen_progress.go never need to know which flow produced a job.
type previewJob struct {
	Label  string
	Result syncengine.PreviewResult
	Start  func(ctx context.Context) (*syncengine.Job, <-chan syncengine.ProgressSnapshot)
}

func showPreview(s *state, jobs []previewJob, onBack func()) {
	var totalBytes int64
	var totalCopy, totalSkip int
	for _, j := range jobs {
		totalBytes += j.Result.TotalBytes
		totalCopy += j.Result.CopyCount
		totalSkip += j.Result.SkipCount
	}

	summary := widget.NewLabel(fmt.Sprintf("%d item(s) · %d files to copy (%s) · %d already identical",
		len(jobs), totalCopy, humanBytes(totalBytes), totalSkip))

	details := widget.NewMultiLineEntry()
	details.Disable()
	details.SetPlaceHolder("Select an item on the left to see its file list")

	list := widget.NewList(
		func() int { return len(jobs) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			j := jobs[id]
			obj.(*widget.Label).SetText(fmt.Sprintf("%s — %d files, %s", j.Label, j.Result.CopyCount, humanBytes(j.Result.TotalBytes)))
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		var b strings.Builder
		for _, e := range jobs[id].Result.Entries {
			action := "copy"
			if e.Action == syncengine.ActionSkipIdentical {
				action = "skip (identical)"
			}
			fmt.Fprintf(&b, "%s  [%s]  %s\n", e.RelPath, humanBytes(e.Size), action)
		}
		details.SetText(b.String())
	}

	confirmBtn := widget.NewButton("Confirm & Copy", func() { showProgress(s, jobs, onBack) })
	confirmBtn.Importance = widget.HighImportance
	if totalCopy == 0 {
		confirmBtn.Disable()
	}
	backBtn := widget.NewButton("Back", onBack)

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Preview (dry run)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			summary,
			widget.NewSeparator(),
		),
		container.NewHBox(confirmBtn, backBtn),
		nil, nil,
		container.NewHSplit(list, container.NewVScroll(details)),
	)
	s.setContent(container.NewPadded(content))
}
