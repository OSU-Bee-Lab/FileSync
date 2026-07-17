package ui

import (
	"image/color"

	"fyne.io/fyne/v2"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
)

// colorRGBA builds an image/color.NRGBA, used for recorderRow status
// background tints (see rowBackgroundColor).
func colorRGBA(r, g, b, a uint8) color.Color {
	return color.NRGBA{R: r, G: g, B: b, A: a}
}

// recorderJobStatus is the row-level lifecycle state shown on Screen 2,
// distinct from recorder.OffloadStatus so the UI can represent states
// (Idle, Disconnected) that don't exist on the offload side.
type recorderJobStatus int

const (
	jobIdle recorderJobStatus = iota
	jobSyncing
	jobConflict
	jobError
	jobDone
	jobDisconnected
)

// recorderRow is one attached (recognized) recorder's live UI state.
// Unrecognized volumes never get a row at all — see the VolumeAttached
// handling in showRecorderSync.
type recorderRow struct {
	volume    recorder.Volume
	driver    recorder.Driver
	id        string
	job       *recorder.OffloadJob
	status    recorderJobStatus
	statusMsg string
	progress  float64
	started   bool // a job was ever started for this row
	done      bool

	// sourceFiles and destDirs are captured at offload start (while the
	// volume is still attached) so bad-timestamp detection can run later,
	// once every recorder this session is idle, without needing the
	// recorder itself - see recorder.CheckRecorderTimestamp/ApplyTimestampFix.
	sourceFiles []recorder.SourceFile
	destDirs    []string
}

// rowStatusText is the default display label for a state, used whenever a
// row doesn't have a more specific statusMsg set.
func rowStatusText(st recorderJobStatus) string {
	switch st {
	case jobIdle:
		return "Idle"
	case jobSyncing:
		return "Syncing"
	case jobConflict:
		return "Conflict"
	case jobError:
		return "Error"
	case jobDone:
		return "Done"
	case jobDisconnected:
		return "Disconnected"
	default:
		return ""
	}
}

// rowStatusMessage is what's actually shown in the status column: row's own
// statusMsg if it set one (e.g. an error detail, or "Done (no files)"),
// otherwise the state's default label.
func rowStatusMessage(row *recorderRow) string {
	if row.statusMsg != "" {
		return row.statusMsg
	}
	return rowStatusText(row.status)
}

// rowBackgroundColor matches the reference (Python/tkinter) implementation's
// status palette: syncing = light teal, conflict = orange, error = red,
// done = blue, disconnected = pink, idle = untinted. blinkOn alternates the
// jobDone color between two shades so finished rows draw the eye.
func rowBackgroundColor(st recorderJobStatus, blinkOn bool) (r, g, b, a uint8) {
	switch st {
	case jobSyncing:
		return 0xC1, 0xDB, 0xD9, 0xFF
	case jobConflict:
		return 0xE0, 0x7B, 0x4A, 0xFF
	case jobError:
		return 0xF0, 0x9B, 0x97, 0xFF
	case jobDone:
		if blinkOn {
			return 0x4A, 0x9D, 0xE0, 0xFF
		}
		return 0xAE, 0xD3, 0xF2, 0xFF
	case jobDisconnected:
		return 0xFF, 0xAD, 0xED, 0xFF
	default: // jobIdle
		return 0, 0, 0, 0
	}
}

// recorderRowLayout gives the recorder ID column a fixed share of the row's
// width (idColRatio), with a floor (minIDColWidth) so it doesn't get
// squeezed to nothing as the window narrows. The remaining width goes to
// the status/progress column.
type recorderRowLayout struct{}

const (
	idColRatio    = 0.15
	minIDColWidth = 90
)

func (recorderRowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) < 2 {
		return fyne.NewSize(0, 0)
	}
	idMin := objects[0].MinSize()
	restMin := objects[1].MinSize()
	h := idMin.Height
	if restMin.Height > h {
		h = restMin.Height
	}
	w := idMin.Width
	if w < minIDColWidth {
		w = minIDColWidth
	}
	return fyne.NewSize(w+restMin.Width, h)
}

func (recorderRowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	if len(objects) < 2 {
		return
	}
	idCol, rest := objects[0], objects[1]
	idWidth := size.Width * idColRatio
	if idWidth < minIDColWidth {
		idWidth = minIDColWidth
	}
	if idWidth > size.Width {
		idWidth = size.Width
	}
	idCol.Move(fyne.NewPos(0, 0))
	idCol.Resize(fyne.NewSize(idWidth, size.Height))
	rest.Move(fyne.NewPos(idWidth, 0))
	rest.Resize(fyne.NewSize(size.Width-idWidth, size.Height))
}
