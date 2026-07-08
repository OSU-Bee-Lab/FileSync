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

// SourceListing is a full recursive listing of one source subtree (an
// experiment, or any relPath under a Location), captured once so it can be
// diffed against multiple destinations without re-walking the source once
// per destination. See ScanExperimentSource /
// ScanSyncExperimentsAgainstSource.
type SourceListing struct {
	objects []fs.Object
	dirs    []string
}

// listSource walks <srcRoot>/<relPath> (through fset's filter) exactly
// once, collecting every file and directory it finds. It performs no
// comparison against any destination.
func listSource(ctx context.Context, srcRoot, relPath string, fset FilterSettings, progress ScanProgressFunc) (SourceListing, error) {
	ctx, err := withFilter(ctx, fset)
	if err != nil {
		return SourceListing{}, err
	}

	fsrc, err := cache.Get(ctx, joinSpec(srcRoot, relPath))
	if err != nil {
		return SourceListing{}, err
	}

	var listing SourceListing
	lastEmit := time.Time{}

	err = walk.ListR(ctx, fsrc, "", false, -1, walk.ListAll, func(entries fs.DirEntries) error {
		for _, entry := range entries {
			switch x := entry.(type) {
			case fs.Directory:
				listing.dirs = append(listing.dirs, x.Remote())
			case fs.Object:
				listing.objects = append(listing.objects, x)
			}
		}
		if progress != nil {
			now := time.Now()
			if lastEmit.IsZero() || now.Sub(lastEmit) >= 100*time.Millisecond {
				lastEmit = now
				progress(ScanProgress{FilesScanned: len(listing.objects), DirsSeen: len(listing.dirs)})
			}
		}
		return nil
	})
	if err != nil {
		return SourceListing{}, err
	}
	return listing, nil
}

// ScanSyncExperiments scans one whole experiment from src to dst
// (Location <-> Location, mirrored under each side's own experiments/
// root). Read-only, safe to call anytime.
func ScanSyncExperiments(ctx context.Context, src, dst Location, experimentName string, fset FilterSettings) (ScanResult, error) {
	return ScanSyncExperimentsWithProgress(ctx, src, dst, experimentName, fset, nil)
}

// ScanSyncExperimentsWithProgress is ScanSyncExperiments with live progress updates.
func ScanSyncExperimentsWithProgress(ctx context.Context, src, dst Location, experimentName string, fset FilterSettings, progress ScanProgressFunc) (ScanResult, error) {
	listing, err := listSource(ctx, src.rcloneSpec(), experimentName, fset, progress)
	if err != nil {
		return ScanResult{}, err
	}
	return scanAgainstDest(ctx, listing, dst.rcloneSpec(), experimentName, experimentName, progress)
}

// ScanExperimentSource walks one experiment's full source file tree exactly
// once. The returned listing can be fed into ScanSyncExperimentsAgainstSource
// for as many destinations as needed, so syncing one experiment to N
// destinations only ever walks the source once instead of N times.
func ScanExperimentSource(ctx context.Context, src Location, experimentName string, fset FilterSettings, progress ScanProgressFunc) (SourceListing, error) {
	return listSource(ctx, src.rcloneSpec(), experimentName, fset, progress)
}

// ScanSyncExperimentsAgainstSource diffs a previously-captured source
// listing (see ScanExperimentSource) against dst, without re-walking the
// source.
func ScanSyncExperimentsAgainstSource(ctx context.Context, listing SourceListing, dst Location, experimentName string, progress ScanProgressFunc) (ScanResult, error) {
	return scanAgainstDest(ctx, listing, dst.rcloneSpec(), experimentName, experimentName, progress)
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
	listing, err := listSource(ctx, src.rcloneSpec(), srcRelPath, fset, progress)
	if err != nil {
		return ScanResult{}, err
	}
	return scanAgainstDest(ctx, listing, destFolder, srcRelPath, label, progress)
}

// scanAgainstDest is the shared scan implementation behind
// ScanSyncExperiments and ScanPullFiles: it diffs a pre-walked source
// listing against <dstRoot>/<relPath>, without transferring anything.
//
// The destination is listed in bulk once (like the source) rather than
// stat'd per file: a per-file fs.Fs.NewObject call is a network round trip
// for cloud remotes, so diffing N source files against a per-file stat
// would mean N destination round trips. Listing once and comparing against
// an in-memory map turns that into a single listing plus in-memory
// comparisons.
func scanAgainstDest(ctx context.Context, listing SourceListing, dstRoot, relPath, label string, progress ScanProgressFunc) (ScanResult, error) {
	fdst, err := cache.Get(ctx, joinSpec(dstRoot, relPath))
	if err != nil {
		return ScanResult{}, err
	}

	dstObjs := make(map[string]fs.Object, len(listing.objects))
	err = walk.ListR(ctx, fdst, "", false, -1, walk.ListObjects, func(entries fs.DirEntries) error {
		for _, entry := range entries {
			if o, ok := entry.(fs.Object); ok {
				dstObjs[o.Remote()] = o
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrorDirNotFound) {
		return ScanResult{}, err
	}
	debugf("scan %s: walking %s against %s", label, joinSpec(dstRoot, relPath), fdst.Root())

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

	for _, dir := range listing.dirs {
		updateSeq++
		ensureDir(dir).UpdatedSeq = updateSeq
		emit(dir, dir, false)
	}

	for _, srcObj := range listing.objects {
		if err := ctx.Err(); err != nil {
			return ScanResult{}, err
		}
		scanOneObject(ctx, dstObjs, srcObj, &result, &recent, ensureDir, &updateSeq)
		emit(parentDir(srcObj.Remote()), srcObj.Remote(), false)
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

func scanOneObject(ctx context.Context, dstObjs map[string]fs.Object, srcObj fs.Object, result *ScanResult, recent *[]ScanEntry, ensureDir func(string) *ScanDirProgress, updateSeq *int) {
	relFile := srcObj.Remote()
	action := ActionCopy

	if dstObj, ok := dstObjs[relFile]; ok && operations.Equal(ctx, srcObj, dstObj) {
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
