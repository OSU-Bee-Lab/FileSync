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

// StartOffload copies every file driver.SourceFiles(v) reports into
// destRoot/experimentName/recorderID/..., verifying each file
// byte-for-byte (see smartcopy.go) before considering it complete. A file
// that already has different content at its destination path is reported
// as an error rather than silently overwritten or auto-resolved — there is
// no interactive conflict-resolution step in this pass.
//
// Each file that reaches verified-complete is immediately queued for
// upload to uploadDest (if non-nil), independent of the other files in
// this recorder and of the eventual delete step below — this is what lets
// cloud upload start well before a whole recorder or session finishes,
// rather than waiting for a separate scan+sync pass afterward.
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
	destRoot string,
	experimentName string,
	uploadDest *syncengine.Location,
	autoDelete bool,
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

		destDir := filepath.Join(destRoot, experimentName, recorderID)
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

			destPath := filepath.Join(destDir, sf.DestRelPath)
			if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
				files[sf.DestRelPath] = FileOffloadProgress{Err: err}
				emit(OffloadError, sf.DestRelPath, err)
				return
			}

			states, err := fileStates(sf.AbsPath, []string{destPath})
			if err != nil {
				files[sf.DestRelPath] = FileOffloadProgress{Err: err}
				emit(OffloadError, sf.DestRelPath, err)
				return
			}

			switch states[destPath] {
			case StateComplete:
				files[sf.DestRelPath] = FileOffloadProgress{State: StateComplete}
				continue
			case StateConflict:
				err := fmt.Errorf("%s already exists at destination with different content", sf.DestRelPath)
				files[sf.DestRelPath] = FileOffloadProgress{Err: err, State: StateConflict}
				emit(OffloadError, sf.DestRelPath, err)
				return
			}

			cp := &CopyProgress{}
			copyDone := make(chan error, 1)
			go func(src, dst string) { copyDone <- smartcopy(src, []string{dst}, cp) }(sf.AbsPath, destPath)

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

			if uploadDest != nil {
				dest := *uploadDest
				go func(localPath, rel string) {
					_ = syncengine.StartFileUpload(context.Background(), localPath, dest, rel)
				}(destPath, filepath.Join(experimentName, recorderID, sf.DestRelPath))
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
