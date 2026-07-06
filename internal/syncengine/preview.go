package syncengine

import (
	"context"
	"errors"
	"path"
	"sort"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/operations"
	"github.com/rclone/rclone/fs/walk"
)

// PreviewAction describes what a copy would do to one file.
type PreviewAction int

const (
	ActionCopy PreviewAction = iota
	ActionSkipIdentical
)

// PreviewEntry is one file a dry-run inspected.
type PreviewEntry struct {
	RelPath string
	Size    int64
	Action  PreviewAction
}

// PreviewResult summarizes a dry-run: what a real copy would transfer.
type PreviewResult struct {
	Entries    []PreviewEntry
	TotalBytes int64
	CopyCount  int
	SkipCount  int
}

// PreviewDirProgress summarizes one directory seen during a dry-run preview.
type PreviewDirProgress struct {
	Path       string
	Files      int
	CopyCount  int
	SkipCount  int
	CopyBytes  int64
	UpdatedSeq int
}

// PreviewProgress is a lightweight live snapshot emitted while a preview is
// walking and comparing files.
type PreviewProgress struct {
	Label        string
	CurrentDir   string
	CurrentPath  string
	FilesScanned int
	DirsSeen     int
	CopyCount    int
	SkipCount    int
	TotalBytes   int64
	Recent       []PreviewEntry
	Dirs         []PreviewDirProgress
	Done         bool
}

// PreviewProgressFunc receives live preview progress. Implementations should
// return quickly; slow UI work should be handed off to the UI thread.
type PreviewProgressFunc func(PreviewProgress)

// PreviewBackup dry-runs syncing one whole experiment from src to dst
// (Location <-> Location, mirrored under each side's own experiments/
// root). Read-only, safe to call anytime.
func PreviewBackup(ctx context.Context, src, dst Location, experimentName string, fset FilterSettings) (PreviewResult, error) {
	return PreviewBackupWithProgress(ctx, src, dst, experimentName, fset, nil)
}

// PreviewBackupWithProgress is PreviewBackup with live progress updates.
func PreviewBackupWithProgress(ctx context.Context, src, dst Location, experimentName string, fset FilterSettings, progress PreviewProgressFunc) (PreviewResult, error) {
	return previewCopyPreserving(ctx, src.rcloneSpec(), dst.rcloneSpec(), experimentName, fset, experimentName, progress)
}

// PreviewDownload dry-runs copying an arbitrary sub-path (any depth: a
// whole experiment, one deployment date, one recorder directory, even a
// single file) from src into destFolder, preserving srcRelPath's structure
// under destFolder rather than flattening. destFolder is a raw local path
// (from an OS folder picker), never a saved Location.
func PreviewDownload(ctx context.Context, src Location, srcRelPath string, destFolder string, fset FilterSettings) (PreviewResult, error) {
	return PreviewDownloadWithProgress(ctx, src, srcRelPath, destFolder, fset, nil)
}

// PreviewDownloadWithProgress is PreviewDownload with live progress updates.
func PreviewDownloadWithProgress(ctx context.Context, src Location, srcRelPath string, destFolder string, fset FilterSettings, progress PreviewProgressFunc) (PreviewResult, error) {
	label := srcRelPath
	if label == "" {
		label = "experiments/"
	}
	return previewCopyPreserving(ctx, src.rcloneSpec(), destFolder, srcRelPath, fset, label, progress)
}

// previewCopyPreserving is the shared dry-run implementation behind both
// PreviewBackup and PreviewDownload: it walks <srcRoot>/<relPath> (through
// fset's filter) and diffs each file against <dstRoot>/<relPath>, without
// transferring anything.
func previewCopyPreserving(ctx context.Context, srcRoot, dstRoot, relPath string, fset FilterSettings, label string, progress PreviewProgressFunc) (PreviewResult, error) {
	ctx, err := withFilter(ctx, fset)
	if err != nil {
		return PreviewResult{}, err
	}

	fsrc, err := cache.Get(ctx, joinSpec(srcRoot, relPath))
	if err != nil {
		return PreviewResult{}, err
	}
	fdst, err := cache.Get(ctx, joinSpec(dstRoot, relPath))
	if err != nil {
		return PreviewResult{}, err
	}

	var result PreviewResult
	var recent []PreviewEntry
	dirStats := map[string]*PreviewDirProgress{}
	dirsSeen := 1
	updateSeq := 0
	lastEmit := time.Time{}

	dirStats["."] = &PreviewDirProgress{Path: "."}

	snapshotDirs := func() []PreviewDirProgress {
		dirs := make([]PreviewDirProgress, 0, len(dirStats))
		for _, d := range dirStats {
			dirs = append(dirs, *d)
		}
		sort.Slice(dirs, func(i, j int) bool {
			if dirs[i].UpdatedSeq == dirs[j].UpdatedSeq {
				return dirs[i].Path < dirs[j].Path
			}
			return dirs[i].UpdatedSeq > dirs[j].UpdatedSeq
		})
		if len(dirs) > 12 {
			dirs = dirs[:12]
		}
		return dirs
	}

	emit := func(currentDir, currentPath string, force bool) {
		if progress == nil {
			return
		}
		now := time.Now()
		if !force && !lastEmit.IsZero() && now.Sub(lastEmit) < 100*time.Millisecond {
			return
		}
		lastEmit = now
		recentCopy := append([]PreviewEntry(nil), recent...)
		progress(PreviewProgress{
			Label:        label,
			CurrentDir:   displayDir(currentDir),
			CurrentPath:  currentPath,
			FilesScanned: result.CopyCount + result.SkipCount,
			DirsSeen:     dirsSeen,
			CopyCount:    result.CopyCount,
			SkipCount:    result.SkipCount,
			TotalBytes:   result.TotalBytes,
			Recent:       recentCopy,
			Dirs:         snapshotDirs(),
		})
	}

	ensureDir := func(dir string) *PreviewDirProgress {
		dir = displayDir(dir)
		if stat, ok := dirStats[dir]; ok {
			return stat
		}
		dirsSeen++
		stat := &PreviewDirProgress{Path: dir}
		dirStats[dir] = stat
		return stat
	}

	err = walk.ListR(ctx, fsrc, "", false, -1, walk.ListAll, func(entries fs.DirEntries) error {
		for _, entry := range entries {
			switch x := entry.(type) {
			case fs.Directory:
				updateSeq++
				ensureDir(x.Remote()).UpdatedSeq = updateSeq
				emit(x.Remote(), x.Remote(), false)
			case fs.Object:
				if err := previewOneObject(ctx, fdst, x, &result, &recent, ensureDir, &updateSeq); err != nil {
					return err
				}
				emit(parentDir(x.Remote()), x.Remote(), false)
			}
		}
		return nil
	})
	if err != nil {
		return PreviewResult{}, err
	}
	if progress != nil {
		progress(PreviewProgress{
			Label:        label,
			FilesScanned: result.CopyCount + result.SkipCount,
			DirsSeen:     dirsSeen,
			CopyCount:    result.CopyCount,
			SkipCount:    result.SkipCount,
			TotalBytes:   result.TotalBytes,
			Recent:       append([]PreviewEntry(nil), recent...),
			Dirs:         snapshotDirs(),
			Done:         true,
		})
	}
	return result, nil
}

func previewOneObject(ctx context.Context, fdst fs.Fs, srcObj fs.Object, result *PreviewResult, recent *[]PreviewEntry, ensureDir func(string) *PreviewDirProgress, updateSeq *int) error {
	relFile := srcObj.Remote()
	action := ActionCopy

	dstObj, err := fdst.NewObject(ctx, relFile)
	switch {
	case err == nil:
		if operations.Equal(ctx, srcObj, dstObj) {
			action = ActionSkipIdentical
		}
	case errors.Is(err, fs.ErrorObjectNotFound):
		// not present at dest yet - stays ActionCopy
	default:
		return err
	}

	entry := PreviewEntry{
		RelPath: relFile,
		Size:    srcObj.Size(),
		Action:  action,
	}
	result.Entries = append(result.Entries, entry)
	*recent = append(*recent, entry)
	if len(*recent) > 10 {
		*recent = (*recent)[len(*recent)-10:]
	}

	*updateSeq++
	dir := ensureDir(parentDir(relFile))
	dir.Files++
	dir.UpdatedSeq = *updateSeq
	if action == ActionCopy {
		result.CopyCount++
		result.TotalBytes += srcObj.Size()
		dir.CopyCount++
		dir.CopyBytes += srcObj.Size()
	} else {
		result.SkipCount++
		dir.SkipCount++
	}
	return nil
}

func parentDir(remote string) string {
	dir := path.Dir(remote)
	if dir == "." {
		return ""
	}
	return dir
}

func displayDir(dir string) string {
	if dir == "" || dir == "." {
		return "."
	}
	return dir
}
