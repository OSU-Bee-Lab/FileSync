package ui

import (
	"context"
	"fmt"
	"image/color"
	"path"
	"sort"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
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
	// Locs holds the Location(s) involved in Start's copy (source and, for
	// Backup, destination) so a failed job can offer to reconnect the right
	// remote instead of just printing rclone's raw error text.
	Locs []syncengine.Location
}

type previewTask struct {
	Label   string
	Locs    []syncengine.Location
	Preview func(ctx context.Context, progress syncengine.PreviewProgressFunc) (syncengine.PreviewResult, error)
	Start   func(ctx context.Context, result syncengine.PreviewResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot)
}

func showPreview(s *state, jobs []previewJob, onBack func()) {
	var totalBytes int64
	var totalCopy, totalSkip int
	for _, j := range jobs {
		totalBytes += j.Result.TotalBytes
		totalCopy += j.Result.CopyCount
		totalSkip += j.Result.SkipCount
	}

	experimentValue := widget.NewLabelWithStyle(fmt.Sprintf("%d", len(jobs)), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	copyValue := widget.NewLabelWithStyle(fmt.Sprintf("%d", totalCopy), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	sameValue := widget.NewLabelWithStyle(fmt.Sprintf("%d", totalSkip), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	bytesValue := widget.NewLabelWithStyle(humanBytes(totalBytes), fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	summaryLabel := widget.NewLabel(fmt.Sprintf("%s checked · %d files ready to copy · %d already identical",
		plural(len(jobs), "experiment"), totalCopy, totalSkip))
	allSyncedMessage := widget.NewLabel("")
	if totalCopy == 0 {
		allSyncedMessage.SetText("Everything is already synced. No files need to be copied.")
	}

	metrics := container.NewGridWithColumns(4,
		metricPanel("Experiments", experimentValue, color.NRGBA{R: 232, G: 240, B: 254, A: 255}),
		metricPanel("To copy", copyValue, color.NRGBA{R: 255, G: 239, B: 219, A: 255}),
		metricPanel("Identical", sameValue, color.NRGBA{R: 232, G: 245, B: 233, A: 255}),
		metricPanel("Bytes", bytesValue, color.NRGBA{R: 243, G: 232, B: 255, A: 255}),
	)

	var selected previewJob
	if len(jobs) > 0 {
		selected = jobs[0]
	}

	var dirRows []syncengine.PreviewDirProgress
	var fileRows []syncengine.PreviewEntry

	list := widget.NewList(
		func() int { return len(jobs) },
		func() fyne.CanvasObject {
			dot := canvas.NewCircle(color.NRGBA{R: 150, G: 154, B: 160, A: 255})
			name := widget.NewLabel("")
			name.Truncation = fyne.TextTruncateEllipsis
			counts := widget.NewLabel("")
			return container.NewBorder(nil, nil, container.NewGridWrap(fyne.NewSize(10, 10), dot), counts, name)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			j := jobs[id]
			box := obj.(*fyne.Container)
			dotWrap := box.Objects[1].(*fyne.Container)
			dot := dotWrap.Objects[0].(*canvas.Circle)
			name := box.Objects[0].(*widget.Label)
			counts := box.Objects[2].(*widget.Label)

			dot.FillColor = experimentColor(j.Result.CopyCount)
			dot.Refresh()
			name.SetText(j.Label)
			counts.SetText(fmt.Sprintf("%d copy / %d same", j.Result.CopyCount, j.Result.SkipCount))
		},
	)

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
			counts.SetText(fmt.Sprintf("%d copy / %d same · %s", row.CopyCount, row.SkipCount, humanBytes(row.CopyBytes)))
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

	selectedTitle := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	selectedSummary := widget.NewLabel("")
	selectedTitle.Truncation = fyne.TextTruncateEllipsis
	selectedSummary.Truncation = fyne.TextTruncateEllipsis

	refreshSelected := func(j previewJob) {
		selected = j
		dirRows = previewDirs(j.Result)
		fileRows = append([]syncengine.PreviewEntry(nil), j.Result.Entries...)
		sort.SliceStable(fileRows, func(i, k int) bool {
			if fileRows[i].Action != fileRows[k].Action {
				return fileRows[i].Action == syncengine.ActionCopy
			}
			return fileRows[i].RelPath < fileRows[k].RelPath
		})
		selectedTitle.SetText(j.Label)
		selectedSummary.SetText(fmt.Sprintf("%d files to copy (%s) · %d already identical",
			j.Result.CopyCount, humanBytes(j.Result.TotalBytes), j.Result.SkipCount))
		dirList.Refresh()
		fileList.Refresh()
	}

	list.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(jobs) {
			refreshSelected(jobs[id])
		}
	}
	if len(jobs) > 0 {
		refreshSelected(selected)
	}

	confirmBtn := widget.NewButton("Confirm & Copy", func() { showProgress(s, jobs, onBack) })
	confirmBtn.Importance = widget.HighImportance
	if totalCopy == 0 {
		confirmBtn.Disable()
	}
	backBtn := widget.NewButton("Back", onBack)

	details := container.NewVSplit(
		container.NewBorder(
			container.NewVBox(selectedTitle, selectedSummary),
			nil, nil, nil,
			dirList,
		),
		container.NewBorder(
			widget.NewLabelWithStyle("Files", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			nil, nil, nil,
			fileList,
		),
	)
	details.Offset = 0.42

	body := container.NewHSplit(
		container.NewBorder(widget.NewLabelWithStyle("Experiments", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), nil, nil, nil, list),
		details,
	)
	body.Offset = 0.34

	content := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Dry run complete", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			summaryLabel,
			allSyncedMessage,
			metrics,
			widget.NewSeparator(),
		),
		container.NewHBox(confirmBtn, backBtn),
		nil, nil,
		body,
	)
	s.setContent(container.NewPadded(content))
	if len(jobs) > 0 {
		list.Select(0)
	}
}

func previewDirs(result syncengine.PreviewResult) []syncengine.PreviewDirProgress {
	dirsByPath := map[string]*syncengine.PreviewDirProgress{}
	for _, entry := range result.Entries {
		dir := path.Dir(entry.RelPath)
		if dir == "." {
			dir = "."
		}
		row, ok := dirsByPath[dir]
		if !ok {
			row = &syncengine.PreviewDirProgress{Path: dir}
			dirsByPath[dir] = row
		}
		row.Files++
		if entry.Action == syncengine.ActionCopy {
			row.CopyCount++
			row.CopyBytes += entry.Size
		} else {
			row.SkipCount++
		}
	}

	out := make([]syncengine.PreviewDirProgress, 0, len(dirsByPath))
	for _, row := range dirsByPath {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CopyCount != out[j].CopyCount {
			return out[i].CopyCount > out[j].CopyCount
		}
		if out[i].CopyBytes != out[j].CopyBytes {
			return out[i].CopyBytes > out[j].CopyBytes
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func experimentColor(copyCount int) color.Color {
	if copyCount > 0 {
		return color.NRGBA{R: 230, G: 126, B: 34, A: 255}
	}
	return color.NRGBA{R: 46, G: 160, B: 67, A: 255}
}

func previewEntryColor(action syncengine.PreviewAction) color.Color {
	if action == syncengine.ActionSkipIdentical {
		return color.NRGBA{R: 46, G: 160, B: 67, A: 255}
	}
	return color.NRGBA{R: 230, G: 126, B: 34, A: 255}
}
