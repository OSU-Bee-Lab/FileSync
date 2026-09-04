package recorder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// DestDirs computes, for each destRoot in destRoots, the recorder's
// destination directory: destRoot/experimentName/subpath/recorderID
// (subpath's components, split via splitSubpath, are skipped if empty).
// Shared by StartOffload and anything else that needs to locate a
// recorder's already-offloaded files without re-running the whole offload
// (e.g. bad-timestamp detection/correction, which runs after StartOffload
// has finished and the recorder itself may already be gone).
func DestDirs(destRoots []string, subpath, experimentName, recorderID string) []string {
	subpathParts := splitSubpath(subpath)
	destDirs := make([]string, len(destRoots))
	for i, root := range destRoots {
		parts := append([]string{root, experimentName}, subpathParts...)
		destDirs[i] = filepath.Join(append(parts, recorderID)...)
	}
	return destDirs
}

// splitSubpath breaks a user-typed subpath into its component directory
// names, accepting either "/" or "\" as a separator regardless of the
// current OS - so a path typed on Windows still nests correctly when the
// destination (or the app itself) is on macOS/Linux, and vice versa. Empty
// components (leading/trailing/doubled separators) are dropped.
func splitSubpath(subpath string) []string {
	parts := strings.FieldsFunc(subpath, func(r rune) bool { return r == '/' || r == '\\' })
	return parts
}

// maxConcurrentUploads bounds simultaneous cloud uploads across the whole
// process (see uploadSem below).
const maxConcurrentUploads = 3

// uploadSem bounds how many cloud uploads run at once across every offload
// running in this process, not just within one StartOffload call. Files
// land locally in bursts (e.g. ~100 in 15 minutes during an active recorder
// sync), and firing an unbounded number of uploads at the remote
// (SharePoint/OneDrive) causes throttling/errors under load; this caps it
// the same way a normal rclone copy batch would be bounded. It is
// deliberately package-level rather than per-run: a sync session offloads a
// whole batch of recorders, often several at once, and a per-run limit
// would multiply by the number of attached recorders - exactly the burst it
// exists to prevent.
var uploadSem = make(chan struct{}, maxConcurrentUploads)

// maxUploadAttempts is how many times uploadWithRetry tries a single file
// upload (including the first attempt) before reporting it failed.
const maxUploadAttempts = 3

// OffloadStatus is the lifecycle state of a running Offload.
type OffloadStatus int

const (
	OffloadRunning OffloadStatus = iota
	OffloadDone
	OffloadError
	OffloadCanceled
	// OffloadConflict is a specific case of OffloadError: a file already
	// exists at (one of) the destination(s) with different content than
	// the source, so it can neither be resumed nor safely overwritten.
	// Reported separately from OffloadError so the UI can label it
	// "Conflict" rather than a generic error.
	OffloadConflict
)

// FileOffloadProgress tracks progress of a single file within an Offload.
type FileOffloadProgress struct {
	BytesDone  int64
	BytesTotal int64
	State      FileState
	Err        error
}

// OffloadProgress is one point-in-time update on a running Offload,
// mirroring the shape of syncengine.ProgressSnapshot so the UI can follow
// the same conventions used for backup/download jobs, even though this
// isn't an rclone copy.
type OffloadProgress struct {
	FilesDone, FilesTotal int
	BytesDone, BytesTotal int64
	CurrentFile           string
	// Phase describes what's happening to CurrentFile right now
	// ("checking", "syncing", "deleting"), so the UI can show more than a
	// generic "Syncing" for the whole run - e.g. the verify-before-copy
	// step and the post-verify recorder cleanup are both silent otherwise.
	Phase  string
	Status OffloadStatus
	Err    error
	Files  map[string]FileOffloadProgress
}

// OffloadJob is a running (or finished) Offload started by StartOffload.
type OffloadJob struct {
	cancel context.CancelFunc
}

// Cancel stops a running OffloadJob. Whatever file is mid-copy is left in
// its partial state on disk — the next attempt resumes it via smartcopy,
// same as after any other interruption.
func (j *OffloadJob) Cancel() {
	j.cancel()
}

// UploadUpdate is a per-file upload lifecycle notification threaded up
// from StartOffload's per-file upload goroutines, one per (destination,
// file) pair, so a UI can build "currently uploading"/"uploaded" lists
// without polling.
//
// DestID/DestName name the upload Location this update is about: with more
// than one upload destination configured, the same recorder+file is
// uploaded once per destination, so (RecorderID, RelPath) alone does not
// identify an upload - a listener keying on it would collapse the two into
// one entry and let the first UploadDone clear the other's.
type UploadUpdate struct {
	RecorderID string
	RelPath    string
	DestID     string
	DestName   string
	Event      syncengine.UploadEvent
	BytesDone  int64
	BytesTotal int64
	Err        error
}

// StartOffload copies every file driver.SourceFiles(v) and
// recorder.MetadataFiles(driver, v) report into
// destRoot/experimentName/subpath/recorderID/... for each destRoot in
// destRoots (subpath is the schema's "intermediate directories", e.g. a
// deployment date or site, and is skipped if empty),
// verifying each file byte-for-byte (see smartcopy.go) before considering
// it complete. A file that already has different content at (any of) its
// destination path(s) is reported as OffloadConflict rather than silently
// overwritten or auto-resolved — there is no interactive
// conflict-resolution step in this pass.
//
// Each file that reaches verified-complete is immediately queued for
// upload to every Location in uploadDests, independent of the other files
// in this recorder and of the eventual delete step below — this is what
// lets cloud upload start well before a whole recorder or session
// finishes, rather than waiting for a separate scan+sync pass afterward.
// onUpload, if non-nil, is called from those upload goroutines with each
// upload's lifecycle events. If batchUpload is set, this per-file queueing
// is skipped entirely — files land locally only, and it's the caller's
// responsibility to push them to uploadDests later (see the Batch Upload
// button on Sync Recorders' active-sync screen).
//
// Once every file is verified complete, source files on the recorder are
// deleted if autoDelete is set - recordings only, never the driver's
// metadata files, which are copied on the same terms as recordings but stay
// on the device (see MetadataFileLister). This is the one place in FileSync that
// deletes data, deliberately: it's the recorder's own storage being reset
// for reuse, not a synced destination, and it only happens after a
// verified copy — see CLAUDE.md for the scoping of the project's
// never-delete rule to the rclone/cloud destination.
//
// ctx and uploadCtx are deliberately separate. ctx governs the local
// copy/verify/delete pass and is what OffloadJob.Cancel cancels — i.e. what
// the caller cancels when this recorder is unplugged mid-transfer, since
// nothing further can be read from a device that's gone. uploadCtx governs
// the cloud uploads of files that already landed and verified locally;
// those have no further need of the recorder, so cancelling the offload
// must not abandon them. Passing the same context for both restores the
// old behavior: unplugging a recorder silently drops every upload it had
// already queued.
func StartOffload(
	ctx context.Context,
	uploadCtx context.Context,
	driver Driver,
	v Volume,
	recorderID string,
	destRoots []string,
	subpath string,
	experimentName string,
	uploadDests []syncengine.Location,
	autoDelete bool,
	batchUpload bool,
	onUpload func(UploadUpdate),
) (*OffloadJob, <-chan OffloadProgress) {
	ctx, cancel := context.WithCancel(ctx)
	progressCh := make(chan OffloadProgress, 1)
	job := &OffloadJob{cancel: cancel}

	go func() {
		defer close(progressCh)

		// Guard the delete path: with no destinations, every file would fall
		// through to "complete" (copied nowhere) and, under autoDelete, its
		// source would be deleted. The UI already prevents this, but the
		// engine must not depend on that.
		if len(destRoots) == 0 {
			progressCh <- OffloadProgress{Status: OffloadError, Err: fmt.Errorf("offload: no destination roots given")}
			return
		}

		// sourceFiles is the recordings - the only files the delete pass
		// below may touch. metadataFiles is the driver's copy-but-never-
		// delete set, appended after the recordings so that everything the
		// device can't regenerate is already safely copied before a
		// metadata conflict (see the conflict handling below) can halt the
		// run. offloadFiles is the two together: the copy/verify/upload
		// pass draws no distinction between them.
		sourceFiles, err := driver.SourceFiles(v)
		if err != nil {
			progressCh <- OffloadProgress{Status: OffloadError, Err: err}
			return
		}
		metadataFiles, err := MetadataFiles(driver, v)
		if err != nil {
			progressCh <- OffloadProgress{Status: OffloadError, Err: err}
			return
		}
		offloadFiles := make([]SourceFile, 0, len(sourceFiles)+len(metadataFiles))
		offloadFiles = append(offloadFiles, sourceFiles...)
		offloadFiles = append(offloadFiles, metadataFiles...)

		subpathParts := splitSubpath(subpath)
		destDirs := DestDirs(destRoots, subpath, experimentName, recorderID)

		// Each source file's size is stat'd upfront so the progress bar's
		// denominator (bytesTotal, summed across files below) is known in full
		// from the very first emit. Otherwise files would only contribute to
		// bytesTotal once their own copy started, so bytesTotal would grow
		// mid-run: the bar could reach 100% on file 1 alone, then drop back
		// down the instant file 2's entry inflated the denominator.
		files := make(map[string]FileOffloadProgress, len(offloadFiles))

		// aggDone/aggBytesDone/aggBytesTotal are running totals mirroring
		// `files`, maintained incrementally by setFile below rather than
		// resummed across the whole map on every emit. An offload can run
		// for hours with a 300ms progress ticker per in-flight file, so a
		// per-emit O(len(files)) resum adds up; setFile keeps each emit O(1)
		// by only adjusting for the one entry that actually changed.
		var aggDone int
		var aggBytesDone, aggBytesTotal int64

		// setFile is the sole way `files` should be mutated below: it
		// updates the map and adjusts the running aggregates by removing the
		// previous entry's contribution (if any) and adding the new one's,
		// so aggDone/aggBytesDone/aggBytesTotal always match the map's
		// contents without ever rescanning it.
		setFile := func(key string, fp FileOffloadProgress) {
			if old, ok := files[key]; ok {
				if old.State == StateComplete {
					aggDone--
				}
				aggBytesDone -= old.BytesDone
				aggBytesTotal -= old.BytesTotal
			}
			files[key] = fp
			if fp.State == StateComplete {
				aggDone++
			}
			aggBytesDone += fp.BytesDone
			aggBytesTotal += fp.BytesTotal
		}

		for _, sf := range offloadFiles {
			var size int64
			if info, err := os.Stat(sf.AbsPath); err == nil {
				size = info.Size()
			}
			setFile(sf.DestRelPath, FileOffloadProgress{BytesTotal: size})
		}

		// emit publishes a progress snapshot. includeFiles controls whether
		// the (cloned) per-file map is attached. Every call site below is a
		// one-per-file transition (checking/complete/conflict/error/
		// deleting/done) except the in-copy ticker further down, which fires
		// every 300ms for however long the current file takes to copy — by
		// far the highest-frequency emit — so that one site skips the
		// clone, since CurrentFile/BytesDone/BytesTotal already carry
		// everything a live progress bar needs.
		emit := func(status OffloadStatus, phase, current string, err error, includeFiles bool) {
			snapshot := OffloadProgress{
				FilesDone:   aggDone,
				FilesTotal:  len(offloadFiles),
				BytesDone:   aggBytesDone,
				BytesTotal:  aggBytesTotal,
				CurrentFile: current,
				Phase:       phase,
				Status:      status,
				Err:         err,
			}
			if includeFiles {
				snapshot.Files = cloneFileProgress(files)
			}
			// A terminal status is sent unconditionally. Racing it against
			// ctx.Done the way progress updates are would let the select drop
			// exactly the outcome the caller is waiting for - on the
			// cancellation path ctx is *always* already done, so an
			// OffloadCanceled had a coin-flip chance of never being
			// delivered, leaving the UI showing the run as still syncing
			// until the channel closed under it. Callers range over
			// progressCh until it closes (see internal/ui's recorder sync
			// screen), so this send always has a receiver.
			if status != OffloadRunning {
				progressCh <- snapshot
				return
			}
			select {
			case progressCh <- snapshot:
			case <-ctx.Done():
			}
		}

		// verifyIdentity re-reads the recorder's own ID off v and confirms it
		// still matches recorderID. It's cheap (one directory listing or one
		// small file read) and is the last line of defense against a device
		// swap mid-offload: a volume can be unplugged and a *different*
		// physical recorder attached at the same OS mount point (e.g. a
		// jostled hub, or two recorders offloaded back-to-back through the
		// same slot) faster than the detach handler can cancel this job's
		// context. Without this check, a stale AbsPath/destPath computed
		// from the original volume could silently read the new device's
		// bytes into the original recorder's destination folder, or (worse,
		// under autoDelete) delete a file on a device that was never
		// verified at all.
		verifyIdentity := func() error {
			gotID, err := driver.RecorderID(v)
			if err != nil {
				return fmt.Errorf("recorder %s: re-checking identity: %w", recorderID, err)
			}
			if gotID != recorderID {
				return fmt.Errorf("recorder %s: device at this mount point now identifies as %q — it was disconnected and replaced mid-sync", recorderID, gotID)
			}
			return nil
		}

		// Files are copied by a small pool of workers rather than strictly one
		// at a time. A single file can't be copied faster than the card's
		// sequential read (smartcopy's pipeline already gets it there), but a
		// recorder holding thousands of small files — an AudioMoth on a duty
		// cycle writes ~9.6 MB per recording, thousands per card — spends a
		// large share of the run inside each file's closing Sync, with the
		// card idle. Keeping a few copies in flight covers that latency.
		// Measured on an AudioMoth card, the pipeline and this pool together
		// took the offload from roughly half the card's sequential read speed
		// to essentially all of it. The pool is deliberately small: the gain
		// is in hiding per-file latency, not in parallel reads (the card
		// saturates at a single reader), and every extra worker is one more
		// partially-written file to resume after an interruption.
		const copyWorkers = 4

		// mu guards everything the workers share: `files` and its aggregates
		// (i.e. every setFile call), inFlight, and the fatal* fields below.
		// emit is always called with mu held, so a snapshot can never catch
		// the per-file map mid-update.
		var mu sync.Mutex

		// inFlightCopy is one file currently being copied, keyed in inFlight
		// by its index in offloadFiles. The ticker below folds each one's live
		// byte count into `files` and names the lowest-indexed one as
		// CurrentFile, so the UI's status line follows the oldest copy still
		// running and advances roughly in order, rather than flickering
		// between whichever workers happened to tick.
		type inFlightCopy struct {
			rel string
			cp  *CopyProgress
		}
		inFlight := make(map[int]*inFlightCopy)

		// runCtx is what the workers copy under. The first one to fail cancels
		// it so the others wind down promptly instead of copying files that
		// are about to be discarded — the concurrent equivalent of the old
		// sequential loop returning on the spot. It descends from ctx, so an
		// outside cancellation (recorder unplugged) still reaches every
		// worker.
		runCtx, runCancel := context.WithCancel(ctx)
		defer runCancel()

		// The first worker to hit a conflict or error records it here and
		// cancels runCtx; the run's outcome is emitted once, after every
		// worker has stopped. Subsequent failures are dropped: with runCtx
		// canceled they're overwhelmingly the cancellation itself, and
		// reporting one of those would mask the cause. Callers must hold mu.
		var fatalStatus OffloadStatus
		var fatalErr error
		var fatalFile string
		fail := func(status OffloadStatus, rel string, err error) {
			if fatalErr == nil {
				fatalStatus, fatalFile, fatalErr = status, rel, err
			}
			runCancel()
		}

		// One ticker for the whole run, rather than the per-file ticker the
		// sequential loop used: with several copies in flight, per-file
		// tickers would emit copyWorkers snapshots per interval, each
		// clobbering the last's CurrentFile.
		tickerStop := make(chan struct{})
		var tickerWG sync.WaitGroup
		tickerWG.Add(1)
		go func() {
			defer tickerWG.Done()
			ticker := time.NewTicker(300 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-tickerStop:
					return
				case <-ticker.C:
					mu.Lock()
					current, lowest := "", -1
					for idx, f := range inFlight {
						setFile(f.rel, FileOffloadProgress{
							BytesDone:  f.cp.ByteCurrent.Load(),
							BytesTotal: f.cp.BytesTotal.Load(),
						})
						if lowest < 0 || idx < lowest {
							current, lowest = f.rel, idx
						}
					}
					if current != "" {
						emit(OffloadRunning, "syncing", current, nil, false)
					}
					mu.Unlock()
				}
			}
		}()

		// copyOne runs the whole per-file pass — identity re-check, state
		// classification, copy, upload queueing — for one file, on a worker
		// goroutine. It reports failures through fail rather than returning
		// them: the run's single outcome is decided after all the workers
		// have finished.
		copyOne := func(idx int, sf SourceFile) {
			if err := verifyIdentity(); err != nil {
				mu.Lock()
				setFile(sf.DestRelPath, FileOffloadProgress{Err: err})
				fail(OffloadError, sf.DestRelPath, err)
				mu.Unlock()
				return
			}

			mu.Lock()
			emit(OffloadRunning, "checking", sf.DestRelPath, nil, true)
			mu.Unlock()

			destPaths := make([]string, len(destDirs))
			for i, dir := range destDirs {
				destPaths[i] = filepath.Join(dir, sf.DestRelPath)
			}
			for _, dp := range destPaths {
				if err := os.MkdirAll(filepath.Dir(dp), 0o755); err != nil {
					mu.Lock()
					setFile(sf.DestRelPath, FileOffloadProgress{Err: err})
					fail(OffloadError, sf.DestRelPath, err)
					mu.Unlock()
					return
				}
			}

			states, err := fileStates(sf.AbsPath, destPaths)
			if err != nil {
				mu.Lock()
				setFile(sf.DestRelPath, FileOffloadProgress{Err: err})
				fail(OffloadError, sf.DestRelPath, err)
				mu.Unlock()
				return
			}

			var pending []string
			conflict := false
			for _, dp := range destPaths {
				switch states[dp] {
				case StateComplete:
					// already done at this destination, nothing to do
				case StateConflict:
					conflict = true
				default:
					pending = append(pending, dp)
				}
			}
			if conflict {
				err := fmt.Errorf("%s already exists at destination with different content", sf.DestRelPath)
				mu.Lock()
				setFile(sf.DestRelPath, FileOffloadProgress{Err: err, State: StateConflict})
				fail(OffloadConflict, sf.DestRelPath, err)
				mu.Unlock()
				return
			}
			if len(pending) == 0 {
				mu.Lock()
				sz := files[sf.DestRelPath].BytesTotal
				setFile(sf.DestRelPath, FileOffloadProgress{State: StateComplete, BytesDone: sz, BytesTotal: sz})
				mu.Unlock()
				return
			}

			cp := &CopyProgress{}
			mu.Lock()
			inFlight[idx] = &inFlightCopy{rel: sf.DestRelPath, cp: cp}
			mu.Unlock()

			copyErr := smartcopy(runCtx, sf.AbsPath, pending, cp)

			mu.Lock()
			delete(inFlight, idx)
			if copyErr != nil {
				// A copy aborted because runCtx was canceled says nothing
				// about this file — it's a consequence of another worker's
				// failure, or of the caller canceling the offload. Recording
				// it would race to become the reported cause and hide the
				// real one.
				if runCtx.Err() == nil {
					setFile(sf.DestRelPath, FileOffloadProgress{Err: copyErr})
					fail(OffloadError, sf.DestRelPath, copyErr)
				}
				mu.Unlock()
				return
			}
			total := cp.BytesTotal.Load()
			setFile(sf.DestRelPath, FileOffloadProgress{State: StateComplete, BytesDone: total, BytesTotal: total})
			emit(OffloadRunning, "syncing", sf.DestRelPath, nil, true)
			mu.Unlock()

			if batchUpload {
				return
			}

			fileTotal := total
			for _, uploadDest := range uploadDests {
				dest := uploadDest
				localPath := destPaths[0]
				relParts := append([]string{experimentName}, subpathParts...)
				relParts = append(relParts, recorderID, sf.DestRelPath)
				rel := filepath.Join(relParts...)
				report := func(ev syncengine.UploadEvent, bytesDone, bytesTotal int64, uerr error) {
					if onUpload == nil {
						return
					}
					onUpload(UploadUpdate{
						RecorderID: recorderID, RelPath: rel,
						DestID: dest.ID, DestName: dest.Name,
						Event: ev, BytesDone: bytesDone, BytesTotal: bytesTotal, Err: uerr,
					})
				}
				report(syncengine.UploadQueued, 0, fileTotal, nil)
				go func(localPath, rel string) {
					select {
					case uploadSem <- struct{}{}:
					case <-uploadCtx.Done():
						// Every queued file must end on a terminal event:
						// a listener adds its entry at UploadQueued, so
						// returning silently here would leave it stuck in
						// the "uploading" list forever - and any caller
						// deriving "is anything still transferring" from
						// that list (see internal/ui's upload panel) would
						// never see the session go idle again.
						report(syncengine.UploadFailed, 0, fileTotal, uploadCtx.Err())
						return
					}
					defer func() { <-uploadSem }()
					uploadWithRetry(uploadCtx, localPath, dest, rel, report)
				}(localPath, rel)
			}
		}

		// Work is handed out in offloadFiles order, so recordings are still
		// picked up before the metadata files appended after them (see
		// offloadFiles above) — with the pool, up to copyWorkers-1 of the last
		// recordings may merely be in flight rather than finished when the
		// first metadata file starts, which is as close to that ordering as
		// concurrency allows and leaves any partial copy resumable anyway.
		type workItem struct {
			idx int
			sf  SourceFile
		}
		work := make(chan workItem)
		var workerWG sync.WaitGroup
		for w := 0; w < copyWorkers; w++ {
			workerWG.Add(1)
			go func() {
				defer workerWG.Done()
				for item := range work {
					if runCtx.Err() != nil {
						return
					}
					copyOne(item.idx, item.sf)
				}
			}()
		}
		go func() {
			defer close(work)
			for i, sf := range offloadFiles {
				select {
				case work <- workItem{idx: i, sf: sf}:
				case <-runCtx.Done():
					return
				}
			}
		}()
		workerWG.Wait()

		close(tickerStop)
		tickerWG.Wait()

		if fatalErr != nil {
			emit(fatalStatus, "", fatalFile, fatalErr, true)
			return
		}
		if err := ctx.Err(); err != nil {
			emit(OffloadCanceled, "", "", err, true)
			return
		}

		allComplete := true
		for _, fp := range files {
			if fp.State != StateComplete {
				allComplete = false
				break
			}
		}

		// Note the range over sourceFiles, not offloadFiles: metadata files
		// were copied and verified alongside the recordings, but deleting
		// them is exactly what MetadataFileLister exists to prevent.
		if autoDelete && allComplete {
			for _, sf := range sourceFiles {
				if ctx.Err() != nil {
					emit(OffloadCanceled, "", sf.DestRelPath, ctx.Err(), true)
					return
				}

				// Re-verify identity before every single deletion, not just
				// once before this loop starts: this is the one place
				// StartOffload deletes source data (see package doc), and a
				// recorder can be unplugged and a *different* device
				// attached at the same mount point in the time it takes to
				// delete a batch of files (see verifyIdentity's doc above).
				// RecorderID is documented as cheap, so there's no reason to
				// trust a check taken before the loop for files deleted
				// later in it. On any verification failure — including the
				// device simply being gone — stop immediately without
				// deleting this or any further file; a failed re-check means
				// the trusted device may no longer be there, so no further
				// deletion is safe.
				if err := verifyIdentity(); err != nil {
					emit(OffloadError, "", sf.DestRelPath, err, true)
					return
				}

				emit(OffloadRunning, "deleting", sf.DestRelPath, nil, true)
				if err := os.Remove(sf.AbsPath); err != nil {
					emit(OffloadError, "", sf.DestRelPath, err, true)
					return
				}
			}
		}

		emit(OffloadDone, "", "", nil, true)
	}()

	return job, progressCh
}

// uploadWithRetry calls syncengine.StartFileUpload, retrying transient
// failures (rate limiting, network blips) up to maxUploadAttempts times with
// a short backoff before giving up. Without this, a single throttled request
// during a burst of recorder uploads reported UploadFailed once and the file
// was never tried again or surfaced to the user.
//
// onEvent is de-duplicated across attempts: UploadStarted is only forwarded
// once (on the first attempt) so the UI doesn't add a duplicate
// "currently uploading" entry per retry, and UploadFailed is only forwarded
// on the final attempt so a retried-then-succeeded upload doesn't flash an
// error.
//
// Exactly one terminal event (UploadDone or UploadFailed) always reaches
// onEvent before this returns. That matters on the cancellation path: a
// failed non-final attempt has its UploadFailed suppressed on the
// assumption another attempt follows, so when ctx is canceled during the
// backoff - and no attempt ever follows - this reports that suppressed
// failure itself rather than returning silently and leaving the caller's
// "uploading" entry stuck forever.
func uploadWithRetry(ctx context.Context, localPath string, dst syncengine.Location, relPath string, onEvent syncengine.UploadProgressFunc) {
	// lastTotal remembers the file size seen by the attempt that just ran,
	// so a failure reported here (rather than by StartFileUpload) still
	// carries the same BytesTotal the caller's entry was created with.
	var lastTotal int64
	for attempt := 1; attempt <= maxUploadAttempts; attempt++ {
		final := attempt == maxUploadAttempts
		wrapped := func(ev syncengine.UploadEvent, bytesDone, bytesTotal int64, uerr error) {
			if bytesTotal > 0 {
				lastTotal = bytesTotal
			}
			if onEvent == nil {
				return
			}
			if ev == syncengine.UploadStarted && attempt > 1 {
				return
			}
			if ev == syncengine.UploadFailed && !final {
				return
			}
			onEvent(ev, bytesDone, bytesTotal, uerr)
		}
		err := syncengine.StartFileUpload(ctx, localPath, dst, relPath, wrapped)
		if err == nil || final {
			return
		}
		select {
		case <-time.After(time.Duration(attempt) * 2 * time.Second):
		case <-ctx.Done():
			if onEvent != nil {
				onEvent(syncengine.UploadFailed, 0, lastTotal, err)
			}
			return
		}
	}
}

// UploadCorrectedFile uploads localPath to dst as relPath, retrying
// transient failures the same way StartOffload's own per-file uploads do
// (see uploadWithRetry), and reports progress via onUpload in the same
// UploadUpdate shape - so a bad-timestamp fix's re-upload (see
// internal/ui's timestamp review screen, reached once every recorder this
// session is idle) shows up in the same upload panel as a normal offload
// upload rather than needing its own UI path. Used only outside batch-upload
// mode: there, a file already uploaded under its bad name the instant it
// landed locally, so correcting it locally afterward doesn't by itself push
// the corrected copy anywhere - this does that push explicitly.
func UploadCorrectedFile(ctx context.Context, recorderID, relPath, localPath string, dst syncengine.Location, onUpload func(UploadUpdate)) {
	report := func(ev syncengine.UploadEvent, bytesDone, bytesTotal int64, uerr error) {
		if onUpload == nil {
			return
		}
		onUpload(UploadUpdate{
			RecorderID: recorderID, RelPath: relPath,
			DestID: dst.ID, DestName: dst.Name,
			Event: ev, BytesDone: bytesDone, BytesTotal: bytesTotal, Err: uerr,
		})
	}
	report(syncengine.UploadQueued, 0, 0, nil)
	// Same process-wide cap as StartOffload's own per-file uploads: a
	// correction can re-upload every file of every recorder in the session
	// at once, which is exactly the burst uploadSem exists to flatten.
	select {
	case uploadSem <- struct{}{}:
	case <-ctx.Done():
		report(syncengine.UploadFailed, 0, 0, ctx.Err())
		return
	}
	defer func() { <-uploadSem }()
	uploadWithRetry(ctx, localPath, dst, relPath, report)
}

func cloneFileProgress(m map[string]FileOffloadProgress) map[string]FileOffloadProgress {
	out := make(map[string]FileOffloadProgress, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
