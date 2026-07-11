package recorder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// splitSubpath breaks a user-typed subpath into its component directory
// names, accepting either "/" or "\" as a separator regardless of the
// current OS - so a path typed on Windows still nests correctly when the
// destination (or the app itself) is on macOS/Linux, and vice versa. Empty
// components (leading/trailing/doubled separators) are dropped.
func splitSubpath(subpath string) []string {
	parts := strings.FieldsFunc(subpath, func(r rune) bool { return r == '/' || r == '\\' })
	return parts
}

// maxConcurrentUploads bounds simultaneous cloud uploads within one
// StartOffload run (see uploadSem below).
const maxConcurrentUploads = 3

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
type UploadUpdate struct {
	RecorderID string
	RelPath    string
	Event      syncengine.UploadEvent
	BytesDone  int64
	BytesTotal int64
	Err        error
}

// StartOffload copies every file driver.SourceFiles(v) reports into
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
// upload's lifecycle events.
//
// Once every file is verified complete, source files on the recorder are
// deleted if autoDelete is set. This is the one place in FileSync that
// deletes data, deliberately: it's the recorder's own storage being reset
// for reuse, not a synced destination, and it only happens after a
// verified copy — see CLAUDE.md for the scoping of the project's
// never-delete rule to the rclone/cloud destination.
func StartOffload(
	ctx context.Context,
	driver Driver,
	v Volume,
	recorderID string,
	destRoots []string,
	subpath string,
	experimentName string,
	uploadDests []syncengine.Location,
	autoDelete bool,
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

		sourceFiles, err := driver.SourceFiles(v)
		if err != nil {
			progressCh <- OffloadProgress{Status: OffloadError, Err: err}
			return
		}

		subpathParts := splitSubpath(subpath)

		destDirs := make([]string, len(destRoots))
		for i, root := range destRoots {
			parts := append([]string{root, experimentName}, subpathParts...)
			destDirs[i] = filepath.Join(append(parts, recorderID)...)
		}

		// Each source file's size is stat'd upfront so the progress bar's
		// denominator (bytesTotal, summed across files below) is known in full
		// from the very first emit. Otherwise files would only contribute to
		// bytesTotal once their own copy started, so bytesTotal would grow
		// mid-run: the bar could reach 100% on file 1 alone, then drop back
		// down the instant file 2's entry inflated the denominator.
		files := make(map[string]FileOffloadProgress, len(sourceFiles))
		for _, sf := range sourceFiles {
			var size int64
			if info, err := os.Stat(sf.AbsPath); err == nil {
				size = info.Size()
			}
			files[sf.DestRelPath] = FileOffloadProgress{BytesTotal: size}
		}

		// uploadSem bounds how many cloud uploads run at once across this
		// whole offload run. Files land locally in bursts (e.g. ~100 in 15
		// minutes during an active recorder sync), and firing an unbounded
		// goroutine per file at the remote (SharePoint/OneDrive) causes
		// throttling/errors under load; this caps it the same way a normal
		// rclone copy batch would be bounded.
		uploadSem := make(chan struct{}, maxConcurrentUploads)

		emit := func(status OffloadStatus, phase, current string, err error) {
			done := 0
			var bytesDone, bytesTotal int64
			for _, fp := range files {
				if fp.State == StateComplete {
					done++
				}
				bytesDone += fp.BytesDone
				bytesTotal += fp.BytesTotal
			}
			snapshot := OffloadProgress{
				FilesDone:   done,
				FilesTotal:  len(sourceFiles),
				BytesDone:   bytesDone,
				BytesTotal:  bytesTotal,
				CurrentFile: current,
				Phase:       phase,
				Status:      status,
				Err:         err,
				Files:       cloneFileProgress(files),
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

		for _, sf := range sourceFiles {
			if ctx.Err() != nil {
				emit(OffloadCanceled, "", sf.DestRelPath, ctx.Err())
				return
			}

			if err := verifyIdentity(); err != nil {
				files[sf.DestRelPath] = FileOffloadProgress{Err: err}
				emit(OffloadError, "", sf.DestRelPath, err)
				return
			}

			emit(OffloadRunning, "checking", sf.DestRelPath, nil)

			destPaths := make([]string, len(destDirs))
			for i, dir := range destDirs {
				destPaths[i] = filepath.Join(dir, sf.DestRelPath)
			}
			for _, dp := range destPaths {
				if err := os.MkdirAll(filepath.Dir(dp), 0o755); err != nil {
					files[sf.DestRelPath] = FileOffloadProgress{Err: err}
					emit(OffloadError, "", sf.DestRelPath, err)
					return
				}
			}

			states, err := fileStates(sf.AbsPath, destPaths)
			if err != nil {
				files[sf.DestRelPath] = FileOffloadProgress{Err: err}
				emit(OffloadError, "", sf.DestRelPath, err)
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
				files[sf.DestRelPath] = FileOffloadProgress{Err: err, State: StateConflict}
				emit(OffloadConflict, "", sf.DestRelPath, err)
				return
			}
			if len(pending) == 0 {
				sz := files[sf.DestRelPath].BytesTotal
				files[sf.DestRelPath] = FileOffloadProgress{State: StateComplete, BytesDone: sz, BytesTotal: sz}
				continue
			}

			cp := &CopyProgress{}
			copyDone := make(chan error, 1)
			go func(src string, dsts []string) { copyDone <- smartcopy(ctx, src, dsts, cp) }(sf.AbsPath, pending)

			ticker := time.NewTicker(300 * time.Millisecond)
		copyLoop:
			for {
				select {
				case err := <-copyDone:
					ticker.Stop()
					if err != nil {
						files[sf.DestRelPath] = FileOffloadProgress{Err: err}
						emit(OffloadError, "", sf.DestRelPath, err)
						return
					}
					break copyLoop
				case <-ticker.C:
					files[sf.DestRelPath] = FileOffloadProgress{BytesDone: cp.ByteCurrent.Load(), BytesTotal: cp.BytesTotal.Load()}
					emit(OffloadRunning, "syncing", sf.DestRelPath, nil)
				case <-ctx.Done():
					ticker.Stop()
					emit(OffloadCanceled, "", sf.DestRelPath, ctx.Err())
					return
				}
			}

			total := cp.BytesTotal.Load()
			files[sf.DestRelPath] = FileOffloadProgress{State: StateComplete, BytesDone: total, BytesTotal: total}
			emit(OffloadRunning, "syncing", sf.DestRelPath, nil)

			fileTotal := total
			for _, uploadDest := range uploadDests {
				dest := uploadDest
				localPath := destPaths[0]
				relParts := append([]string{experimentName}, subpathParts...)
				relParts = append(relParts, recorderID, sf.DestRelPath)
				rel := filepath.Join(relParts...)
				if onUpload != nil {
					onUpload(UploadUpdate{RecorderID: recorderID, RelPath: rel, Event: syncengine.UploadQueued, BytesTotal: fileTotal})
				}
				go func(localPath, rel string) {
					select {
					case uploadSem <- struct{}{}:
					case <-ctx.Done():
						return
					}
					defer func() { <-uploadSem }()
					uploadWithRetry(ctx, localPath, dest, rel, func(ev syncengine.UploadEvent, bytesDone, bytesTotal int64, uerr error) {
						if onUpload != nil {
							onUpload(UploadUpdate{RecorderID: recorderID, RelPath: rel, Event: ev, BytesDone: bytesDone, BytesTotal: bytesTotal, Err: uerr})
						}
					})
				}(localPath, rel)
			}
		}

		allComplete := true
		for _, fp := range files {
			if fp.State != StateComplete {
				allComplete = false
				break
			}
		}

		if autoDelete && allComplete {
			// Re-verify identity once more right before deleting anything:
			// this is the one place StartOffload deletes source data (see
			// package doc), so it must never run against a device that
			// swapped in after the last file's copy completed.
			if err := verifyIdentity(); err != nil {
				emit(OffloadError, "", "", err)
				return
			}
			for _, sf := range sourceFiles {
				if ctx.Err() != nil {
					emit(OffloadCanceled, "", sf.DestRelPath, ctx.Err())
					return
				}
				emit(OffloadRunning, "deleting", sf.DestRelPath, nil)
				if err := os.Remove(sf.AbsPath); err != nil {
					emit(OffloadError, "", sf.DestRelPath, err)
					return
				}
			}
		}

		emit(OffloadDone, "", "", nil)
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
func uploadWithRetry(ctx context.Context, localPath string, dst syncengine.Location, relPath string, onEvent syncengine.UploadProgressFunc) {
	for attempt := 1; attempt <= maxUploadAttempts; attempt++ {
		final := attempt == maxUploadAttempts
		wrapped := func(ev syncengine.UploadEvent, bytesDone, bytesTotal int64, uerr error) {
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
			return
		}
	}
}

func cloneFileProgress(m map[string]FileOffloadProgress) map[string]FileOffloadProgress {
	out := make(map[string]FileOffloadProgress, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
