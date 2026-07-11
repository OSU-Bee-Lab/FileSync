package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// uploadFileEntry is one file's cloud-upload state, shown as a flat row
// (keyed by relPath) in the split upload panel's queued/uploaded lists.
type uploadFileEntry struct {
	recorderID string
	relPath    string
	bytesDone  int64
	bytesTotal int64
	err        error // set when the upload failed after retries
}

func (e uploadFileEntry) label() string {
	return e.relPath
}

func findUploadEntry(list []uploadFileEntry, recorderID, relPath string) int {
	for i, x := range list {
		if x.recorderID == recorderID && x.relPath == relPath {
			return i
		}
	}
	return -1
}

func removeUploadEntry(list []uploadFileEntry, recorderID, relPath string) []uploadFileEntry {
	if i := findUploadEntry(list, recorderID, relPath); i >= 0 {
		return append(list[:i], list[i+1:]...)
	}
	return list
}

// recorderUploadPanel holds the "Upload queue" / "Uploaded" widget.Lists
// shown on Screen 2 when params.uploads is non-empty, and the two slices
// backing them. onUploadEvent is the recorder.UploadUpdate callback wired
// into recorder.StartOffload; it must be called from the Fyne UI thread
// (via fyne.Do), matching every other mutation of showRecorderSync's state.
type recorderUploadPanel struct {
	uploading, uploaded         []uploadFileEntry
	uploadingList, uploadedList *widget.List
	win                         fyne.Window
}

func newRecorderUploadPanel(win fyne.Window) *recorderUploadPanel {
	p := &recorderUploadPanel{win: win}
	p.uploadingList = widget.NewList(
		func() int { return len(p.uploading) },
		func() fyne.CanvasObject { return createBackingBarItem() },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			e := p.uploading[id]
			prog := 0.0
			if e.bytesTotal > 0 {
				prog = float64(e.bytesDone) / float64(e.bytesTotal)
			}
			summary := fmt.Sprintf("%s / %s", humanBytes(e.bytesDone), humanBytes(e.bytesTotal))
			updateBackingBarItem(obj, e.label(), summary, prog, nil, false, false, false, p.win)
		},
	)
	p.uploadedList = widget.NewList(
		func() int { return len(p.uploaded) },
		func() fyne.CanvasObject { return createBackingBarItem() },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			e := p.uploaded[id]
			summary := humanBytes(e.bytesTotal)
			if e.err != nil {
				summary = "Failed"
			}
			updateBackingBarItem(obj, e.label(), summary, 1.0, e.err, e.err != nil, false, false, p.win)
		},
	)
	return p
}

// onUploadEvent is the recorder.UploadUpdate callback handed directly to
// recorder.StartOffload for every offload job on this screen; it hops onto
// the Fyne UI thread itself (via fyne.Do), so the caller doesn't need to.
func (p *recorderUploadPanel) onUploadEvent(u recorder.UploadUpdate) {
	fyne.Do(func() {
		switch u.Event {
		case syncengine.UploadQueued:
			// Added to the queue as soon as the file is offloaded and
			// verified, even though the upload itself may not start yet
			// (see uploadSem in offload.go) - previously the list only
			// picked files up once a slot freed, so it silently topped
			// out at maxConcurrentUploads entries no matter how many
			// files were actually waiting.
			p.uploading = append(p.uploading, uploadFileEntry{
				recorderID: u.RecorderID, relPath: u.RelPath,
				bytesTotal: u.BytesTotal,
			})
		case syncengine.UploadStarted:
			// Entry already added at UploadQueued; nothing to do.
		case syncengine.UploadProgress:
			if i := findUploadEntry(p.uploading, u.RecorderID, u.RelPath); i >= 0 {
				p.uploading[i].bytesDone = u.BytesDone
				p.uploading[i].bytesTotal = u.BytesTotal
			}
		case syncengine.UploadDone:
			p.uploading = removeUploadEntry(p.uploading, u.RecorderID, u.RelPath)
			p.uploaded = append(p.uploaded, uploadFileEntry{
				recorderID: u.RecorderID, relPath: u.RelPath,
				bytesDone: u.BytesTotal, bytesTotal: u.BytesTotal,
			})
		case syncengine.UploadFailed:
			p.uploading = removeUploadEntry(p.uploading, u.RecorderID, u.RelPath)
			// Surface the failure in the "Uploaded" list (flagged red,
			// with the error detail) instead of letting it vanish
			// silently - previously a failed upload disappeared from
			// both lists with no indication to the user.
			p.uploaded = append(p.uploaded, uploadFileEntry{
				recorderID: u.RecorderID, relPath: u.RelPath,
				bytesTotal: u.BytesTotal, err: u.Err,
			})
		}
		if p.uploadingList != nil {
			p.uploadingList.Refresh()
			p.uploadedList.Refresh()
		}
	})
}

// panel lays out the upload queue over the uploaded list, used when
// params.uploads is non-empty.
func (p *recorderUploadPanel) panel() fyne.CanvasObject {
	split := container.NewVSplit(
		container.NewBorder(sectionHeader("Upload queue"), nil, nil, nil, p.uploadingList),
		container.NewBorder(sectionHeader("Uploaded"), nil, nil, nil, p.uploadedList),
	)
	split.SetOffset(0.5)
	return split
}
