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

// ScanAction describes what a copy would do to one file.
type ScanAction int

const (
	ActionCopy ScanAction = iota
	ActionSkipIdentical
)

// ScanEntry is one file a scan inspected.
type ScanEntry struct {
	RelPath string
	Size    int64
	Action  ScanAction
}

// ScanResult summarizes a scan: what a real copy would transfer.
type ScanResult struct {
	Entries    []ScanEntry
	TotalBytes int64
	CopyCount  int
	SkipCount  int
}

// ScanDirProgress summarizes one directory seen during a scan.
type ScanDirProgress struct {
	Path       string
	Files      int
	CopyCount  int
	SkipCount  int
	CopyBytes  int64
	UpdatedSeq int
}

// ScanProgress is a lightweight live snapshot emitted while a scan is
// walking and comparing files.
type ScanProgress struct {
	Label        string
	CurrentDir   string
	CurrentPath  string
	FilesScanned int
	DirsSeen     int
	CopyCount    int
	SkipCount    int
	TotalBytes   int64
	// Recent is every entry inspected so far this scan (not just the
	// last few), so the UI can render the full per-folder file list live.
	Recent []ScanEntry
	Dirs   []ScanDirProgress
	Done   bool
}

// ScanProgressFunc receives live scan progress. Implementations should
// return quickly; slow UI work should be handed off to the UI thread.
type ScanProgressFunc func(ScanProgress)

// ScanSyncExperiments scans one whole experiment from src to dst
// (Location <-> Location, mirrored under each side's own experiments/
// root). Read-only, safe to call anytime.
func ScanSyncExperiments(ctx context.Context, src, dst Location, experimentName string, fset FilterSettings) (ScanResult, error) {
	return ScanSyncExperimentsWithProgress(ctx, src, dst, experimentName, fset, nil)
}

// ScanSyncExperimentsWithProgress is ScanSyncExperiments with live progress updates.
func ScanSyncExperimentsWithProgress(ctx context.Context, src, dst Location, experimentName string, fset FilterSettings, progress ScanProgressFunc) (ScanResult, error) {
	return scanCopyPreserving(ctx, src.rcloneSpec(), dst.rcloneSpec(), experimentName, fset, experimentName, progress)
}

// ScanPullFiles scans an arbitrary sub-path (any depth: a
// whole experiment, one deployment date, one recorder directory, even a
// single file) from src into destFolder, preserving srcRelPath's structure
// under destFolder rather than flattening. destFolder is a raw local path
// (from an OS folder picker), never a saved Location.
func ScanPullFiles(ctx context.Context, src Location, srcRelPath string, destFolder string, fset FilterSettings) (ScanResult, error) {
	return ScanPullFilesWithProgress(ctx, src, srcRelPath, destFolder, fset, nil)
}

// ScanPullFilesWithProgress is ScanPullFiles with live progress updates.
func ScanPullFilesWithProgress(ctx context.Context, src Location, srcRelPath string, destFolder string, fset FilterSettings, progress ScanProgressFunc) (ScanResult, error) {
	label := srcRelPath
	if label == "" {
		label = "experiments/"
	}
	return scanCopyPreserving(ctx, src.rcloneSpec(), destFolder, srcRelPath, fset, label, progress)
}

// scanCopyPreserving is the shared scan implementation behind both
// ScanSyncExperiments and ScanPullFiles: it walks <srcRoot>/<relPath> (through
// fset's filter) and diffs each file against <dstRoot>/<relPath>, without
// transferring anything.
func scanCopyPreserving(ctx context.Context, srcRoot, dstRoot, relPath string, fset FilterSettings, label string, progress ScanProgressFunc) (ScanResult, error) {
	ctx, err := withFilter(ctx, fset)
	if err != nil {
		return ScanResult{}, err
	}

	fsrc, err := cache.Get(ctx, joinSpec(srcRoot, relPath))
	if err != nil {
		return ScanResult{}, err
	}
	fdst, err := cache.Get(ctx, joinSpec(dstRoot, relPath))
	if err != nil {
		return ScanResult{}, err
	}
	debugf("scan %s: walking %s against %s", label, fsrc.Root(), fdst.Root())

	// List the whole destination tree up front into an in-memory map so
	// diffing each source file is a local lookup rather than a per-file
	// network round-trip (fdst.NewObject) against the destination. This
	// matters most for cloud remotes (SharePoint/OneDrive, etc.) where
	// per-call latency otherwise dominates the scan.
	dstObjects := map[string]fs.Object{}
	err = walk.ListR(ctx, fdst, "", false, -1, walk.ListObjects, func(entries fs.DirEntries) error {
		for _, entry := range entries {
			if obj, ok := entry.(fs.Object); ok {
				dstObjects[obj.Remote()] = obj
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrorDirNotFound) {
		return ScanResult{}, err
	}

	var result ScanResult
	var recent []ScanEntry
	dirStats := map[string]*ScanDirProgress{}
	dirsSeen := 1
	updateSeq := 0
	lastEmit := time.Time{}

	dirStats["."] = &ScanDirProgress{Path: "."}

	snapshotDirs := func() []ScanDirProgress {
		dirs := make([]ScanDirProgress, 0, len(dirStats))
		for _, d := range dirStats {
			dirs = append(dirs, *d)
		}
		// Stable path ordering so the folder list doesn't reshuffle as the
		// scan progresses (the user needs to click folders mid-scan).
		sort.Slice(dirs, func(i, j int) bool {
			return dirs[i].Path < dirs[j].Path
		})
		return dirs
	}

	emit := func(currentDir, currentPath string, force bool) {
		if currentPath != "" {
			debugf("scan %s: checking %s", label, currentPath)
		}
		if progress == nil {
			return
		}
		now := time.Now()
		if !force && !lastEmit.IsZero() && now.Sub(lastEmit) < 100*time.Millisecond {
			return
		}
		lastEmit = now
		recentCopy := append([]ScanEntry(nil), recent...)
		progress(ScanProgress{
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

	ensureDir := func(dir string) *ScanDirProgress {
		dir = displayDir(dir)
		if stat, ok := dirStats[dir]; ok {
			return stat
		}
		dirsSeen++
		stat := &ScanDirProgress{Path: dir}
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
				if err := scanOneObject(ctx, dstObjects, x, &result, &recent, ensureDir, &updateSeq); err != nil {
					return err
				}
				emit(parentDir(x.Remote()), x.Remote(), false)
			}
		}
		return nil
	})
	if err != nil {
		return ScanResult{}, err
	}
	debugf("scan %s: done, %d to copy, %d identical", label, result.CopyCount, result.SkipCount)
	if progress != nil {
		progress(ScanProgress{
			Label:        label,
			FilesScanned: result.CopyCount + result.SkipCount,
			DirsSeen:     dirsSeen,
			CopyCount:    result.CopyCount,
			SkipCount:    result.SkipCount,
			TotalBytes:   result.TotalBytes,
			Recent:       append([]ScanEntry(nil), recent...),
			Dirs:         snapshotDirs(),
			Done:         true,
		})
	}
	return result, nil
}

func scanOneObject(ctx context.Context, dstObjects map[string]fs.Object, srcObj fs.Object, result *ScanResult, recent *[]ScanEntry, ensureDir func(string) *ScanDirProgress, updateSeq *int) error {
	relFile := srcObj.Remote()
	action := ActionCopy

	if dstObj, ok := dstObjects[relFile]; ok && operations.Equal(ctx, srcObj, dstObj) {
		action = ActionSkipIdentical
	}

	entry := ScanEntry{
		RelPath: relFile,
		Size:    srcObj.Size(),
		Action:  action,
	}
	result.Entries = append(result.Entries, entry)
	*recent = append(*recent, entry)

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
