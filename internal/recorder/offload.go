package recorder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

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
	Status                OffloadStatus
	Err                   error
	Files                 map[string]FileOffloadProgress
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
	Err        error
}

// StartOffload copies every file driver.SourceFiles(v) reports into
// destRoot/experimentName/recorderID/... for each destRoot in destRoots,
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
// deleted if autoDelete is set. This is the one place in ExpSync that
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

		sourceFiles, err := driver.SourceFiles(v)
		if err != nil {
			progressCh <- OffloadProgress{Status: OffloadError, Err: err}
			return
		}

		destDirs := make([]string, len(destRoots))
		for i, root := range destRoots {
			destDirs[i] = filepath.Join(root, experimentName, recorderID)
		}
		files := make(map[string]FileOffloadProgress, len(sourceFiles))

		emit := func(status OffloadStatus, current string, err error) {
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
				Status:      status,
				Err:         err,
				Files:       cloneFileProgress(files),
			}
			select {
			case progressCh <- snapshot:
			case <-ctx.Done():
			}
		}

		for _, sf := range sourceFiles {
			if ctx.Err() != nil {
				emit(OffloadCanceled, sf.DestRelPath, ctx.Err())
				return
			}

			destPaths := make([]string, len(destDirs))
			for i, dir := range destDirs {
				destPaths[i] = filepath.Join(dir, sf.DestRelPath)
			}
			for _, dp := range destPaths {
				if err := os.MkdirAll(filepath.Dir(dp), 0o755); err != nil {
					files[sf.DestRelPath] = FileOffloadProgress{Err: err}
					emit(OffloadError, sf.DestRelPath, err)
					return
				}
			}

			states, err := fileStates(sf.AbsPath, destPaths)
			if err != nil {
				files[sf.DestRelPath] = FileOffloadProgress{Err: err}
				emit(OffloadError, sf.DestRelPath, err)
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
				emit(OffloadConflict, sf.DestRelPath, err)
				return
			}
			if len(pending) == 0 {
				files[sf.DestRelPath] = FileOffloadProgress{State: StateComplete}
				continue
			}

			cp := &CopyProgress{}
			copyDone := make(chan error, 1)
			go func(src string, dsts []string) { copyDone <- smartcopy(src, dsts, cp) }(sf.AbsPath, pending)

			ticker := time.NewTicker(300 * time.Millisecond)
		copyLoop:
			for {
				select {
				case err := <-copyDone:
					ticker.Stop()
					if err != nil {
						files[sf.DestRelPath] = FileOffloadProgress{Err: err}
						emit(OffloadError, sf.DestRelPath, err)
						return
					}
					break copyLoop
				case <-ticker.C:
					files[sf.DestRelPath] = FileOffloadProgress{BytesDone: cp.ByteCurrent, BytesTotal: cp.BytesTotal}
					emit(OffloadRunning, sf.DestRelPath, nil)
				case <-ctx.Done():
					ticker.Stop()
					emit(OffloadCanceled, sf.DestRelPath, ctx.Err())
					return
				}
			}

			files[sf.DestRelPath] = FileOffloadProgress{State: StateComplete, BytesDone: cp.BytesTotal, BytesTotal: cp.BytesTotal}
			emit(OffloadRunning, sf.DestRelPath, nil)

			for _, uploadDest := range uploadDests {
				dest := uploadDest
				localPath := destPaths[0]
				rel := filepath.Join(experimentName, recorderID, sf.DestRelPath)
				go func(localPath, rel string) {
					_ = syncengine.StartFileUpload(context.Background(), localPath, dest, rel, func(ev syncengine.UploadEvent, uerr error) {
						if onUpload != nil {
							onUpload(UploadUpdate{RecorderID: recorderID, RelPath: rel, Event: ev, Err: uerr})
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
			for _, sf := range sourceFiles {
				if err := os.Remove(sf.AbsPath); err != nil {
					emit(OffloadError, sf.DestRelPath, err)
					return
				}
			}
		}

		emit(OffloadDone, "", nil)
	}()

	return job, progressCh
}

func cloneFileProgress(m map[string]FileOffloadProgress) map[string]FileOffloadProgress {
	out := make(map[string]FileOffloadProgress, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
