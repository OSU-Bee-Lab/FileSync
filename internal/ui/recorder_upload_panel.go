package ui

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/recorder"
	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// uploadFileEntry is one file's cloud-upload state at one destination,
// shown as a flat row in the split upload panel's queued/uploaded lists.
// An entry is keyed by (recorderID, destID, relPath), not by relPath
// alone: with two upload destinations configured the same file uploads
// once per destination, and keying without destID would collapse both into
// one row whose first UploadDone removed the other's entry too.
type uploadFileEntry struct {
	recorderID string
	relPath    string
	destID     string
	destName   string
	bytesDone  int64
	bytesTotal int64
	err        error // set when the upload failed after retries
}

// label names the file, qualified by its destination only once more than
// one destination is in play (see recorderUploadPanel.destIDs) - with the
// single upload destination of a typical session, the suffix would be the
// same on every row and just crowd out the path.
func (e uploadFileEntry) label(showDest bool) string {
	if showDest && e.destName != "" {
		return e.relPath + " → " + e.destName
	}
	return e.relPath
}

func findUploadEntry(list []uploadFileEntry, recorderID, destID, relPath string) int {
	for i, x := range list {
		if x.recorderID == recorderID && x.destID == destID && x.relPath == relPath {
			return i
		}
	}
	return -1
}

func removeUploadEntry(list []uploadFileEntry, recorderID, destID, relPath string) []uploadFileEntry {
	if i := findUploadEntry(list, recorderID, destID, relPath); i >= 0 {
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
	// onChange, if set, is called whenever an entry joins or leaves
	// uploading/uploaded, so a caller tracking "is anything actively
	// transferring" (e.g. recorderSyncScreen.refreshCancelBtn) stays
	// current. Progress ticks don't call it - they can't change that
	// answer, and they're by far the most frequent event.
	onChange func()

	// destIDs is every upload destination seen this session, so label()
	// only qualifies rows with a destination name once there's more than
	// one to tell apart.
	destIDs map[string]bool

	// lastRefresh/refreshScheduled coalesce list refreshes - see
	// requestRefresh. Only ever touched on the Fyne UI thread.
	lastRefresh      time.Time
	refreshScheduled bool
}

// uploadListRefreshInterval is the shortest gap between two upload-list
// refreshes driven by progress ticks. Each in-flight upload reports
// progress every 300ms, and with a batch of recorders offloading at once
// (each file uploading to each destination) those add up to a refresh
// storm of two full list rebuilds apiece, queued onto the Fyne thread
// faster than it can drain them - the queue is unbounded, so it grows
// until the app is out of memory. Membership changes still refresh
// immediately; only the progress-driven ones are rate-limited.
const uploadListRefreshInterval = 200 * time.Millisecond

func newRecorderUploadPanel(win fyne.Window) *recorderUploadPanel {
	p := &recorderUploadPanel{win: win, destIDs: map[string]bool{}}
	p.uploadingList = widget.NewList(
		func() int { return len(p.uploading) },
		func() fyne.CanvasObject { return createBackingBarItem(p.win) },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			e := p.uploading[id]
			prog := 0.0
			if e.bytesTotal > 0 {
				prog = float64(e.bytesDone) / float64(e.bytesTotal)
			}
			summary := fmt.Sprintf("%s / %s", humanBytes(e.bytesDone), humanBytes(e.bytesTotal))
			updateBackingBarItem(obj, e.label(p.showDest()), summary, prog, nil, false, false, false, p.win, "", syncengine.RoleAudio)
		},
	)
	p.uploadedList = widget.NewList(
		func() int { return len(p.uploaded) },
		func() fyne.CanvasObject { return createBackingBarItem(p.win) },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			e := p.uploaded[id]
			summary := humanBytes(e.bytesTotal)
			if e.err != nil {
				summary = "Failed"
			}
			updateBackingBarItem(obj, e.label(p.showDest()), summary, 1.0, e.err, e.err != nil, false, false, p.win, "", syncengine.RoleAudio)
		},
	)
	return p
}

// showDest reports whether rows should name their upload destination.
func (p *recorderUploadPanel) showDest() bool { return len(p.destIDs) > 1 }

// refreshLists redraws both lists now, resetting the coalescing window.
func (p *recorderUploadPanel) refreshLists() {
	p.lastRefresh = time.Now()
	p.refreshScheduled = false
	if p.uploadingList != nil {
		p.uploadingList.Refresh()
		p.uploadedList.Refresh()
	}
}

// requestRefresh redraws both lists, either immediately (membership
// changed, so a stale list would show a wrong row count) or at most once
// per uploadListRefreshInterval (a progress tick, where a slightly stale
// byte count costs nothing). A rate-limited tick that finds a refresh
// already scheduled just drops - the scheduled one will pick its value up.
func (p *recorderUploadPanel) requestRefresh(immediate bool) {
	if immediate {
		p.refreshLists()
		return
	}
	if elapsed := time.Since(p.lastRefresh); elapsed >= uploadListRefreshInterval {
		p.refreshLists()
		return
	}
	if p.refreshScheduled {
		return
	}
	p.refreshScheduled = true
	time.AfterFunc(uploadListRefreshInterval-time.Since(p.lastRefresh), func() {
		fyne.Do(p.refreshLists)
	})
}

// onUploadEvent is the recorder.UploadUpdate callback handed directly to
// recorder.StartOffload for every offload job on this screen; it hops onto
// the Fyne UI thread itself (via fyne.Do), so the caller doesn't need to.
func (p *recorderUploadPanel) onUploadEvent(u recorder.UploadUpdate) {
	fyne.Do(func() {
		if u.DestID != "" {
			p.destIDs[u.DestID] = true
		}
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
				destID: u.DestID, destName: u.DestName,
				bytesTotal: u.BytesTotal,
			})
		case syncengine.UploadStarted:
			// Entry already added at UploadQueued; nothing to do.
		case syncengine.UploadProgress:
			if i := findUploadEntry(p.uploading, u.RecorderID, u.DestID, u.RelPath); i >= 0 {
				p.uploading[i].bytesDone = u.BytesDone
				p.uploading[i].bytesTotal = u.BytesTotal
			}
		case syncengine.UploadDone:
			p.uploading = removeUploadEntry(p.uploading, u.RecorderID, u.DestID, u.RelPath)
			p.uploaded = append(p.uploaded, uploadFileEntry{
				recorderID: u.RecorderID, relPath: u.RelPath,
				destID: u.DestID, destName: u.DestName,
				bytesDone: u.BytesTotal, bytesTotal: u.BytesTotal,
			})
		case syncengine.UploadFailed:
			p.uploading = removeUploadEntry(p.uploading, u.RecorderID, u.DestID, u.RelPath)
			// Surface the failure in the "Uploaded" list (flagged red,
			// with the error detail) instead of letting it vanish
			// silently - previously a failed upload disappeared from
			// both lists with no indication to the user.
			p.uploaded = append(p.uploaded, uploadFileEntry{
				recorderID: u.RecorderID, relPath: u.RelPath,
				destID: u.DestID, destName: u.DestName,
				bytesTotal: u.BytesTotal, err: u.Err,
			})
		}
		membershipChanged := u.Event != syncengine.UploadProgress && u.Event != syncengine.UploadStarted
		p.requestRefresh(membershipChanged)
		if membershipChanged && p.onChange != nil {
			p.onChange()
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
