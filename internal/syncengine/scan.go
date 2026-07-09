package syncengine

import (
	"context"
	"errors"
	"path"
	"sort"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/walk"
)

// ScanAction describes what a copy would do to one file.
type ScanAction int

const (
	ActionCopy ScanAction = iota
	ActionSkipIdentical
	// ActionConflict is a colliding file whose size and/or leading bytes
	// don't both match — see compareObjects. Never auto-copied or
	// auto-skipped; needs user resolution.
	ActionConflict
)

// ScanEntry is one file a scan inspected.
type ScanEntry struct {
	RelPath string
	Size    int64
	Action  ScanAction
	// DstSize and ConflictReason are only populated when Action is
	// ActionConflict — the destination file's size and a short
	// human-readable explanation of why it didn't match (see
	// compareObjects), for display in a conflict-resolution prompt.
	DstSize        int64
	ConflictReason string
}

// ScanResult summarizes a scan: what a real copy would transfer.
type ScanResult struct {
	Entries       []ScanEntry
	TotalBytes    int64
	CopyCount     int
	SkipCount     int
	ConflictCount int
}

// ScanDirProgress summarizes one directory seen during a scan.
type ScanDirProgress struct {
	Path          string
	Files         int
	CopyCount     int
	SkipCount     int
	ConflictCount int
	CopyBytes     int64
	UpdatedSeq    int
}

// ScanProgress is a lightweight live snapshot emitted while a scan is
// walking and comparing files.
type ScanProgress struct {
	Label         string
	CurrentDir    string
	CurrentPath   string
	FilesScanned  int
	DirsSeen      int
	CopyCount     int
	SkipCount     int
	ConflictCount int
	TotalBytes    int64
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

// scanTracker accumulates the per-entry and per-directory bookkeeping every
// scan needs to emit live ScanProgress snapshots and a final ScanResult. It
// is shared between the pairwise scan (scanAgainstDest) and the N-way diff
// (diffNWay) so both drive the exact same live UI.
type scanTracker struct {
	label    string
	progress ScanProgressFunc

	result    ScanResult
	recent    []ScanEntry
	dirStats  map[string]*ScanDirProgress
	dirsSeen  int
	updateSeq int
	lastEmit  time.Time
}

func newScanTracker(label string, progress ScanProgressFunc) *scanTracker {
	return &scanTracker{
		label:    label,
		progress: progress,
		dirStats: map[string]*ScanDirProgress{".": {Path: "."}},
		dirsSeen: 1,
	}
}

func (t *scanTracker) ensureDir(dir string) *ScanDirProgress {
	dir = displayDir(dir)
	if stat, ok := t.dirStats[dir]; ok {
		return stat
	}
	t.dirsSeen++
	stat := &ScanDirProgress{Path: dir}
	t.dirStats[dir] = stat
	return stat
}

// noteDir records a directory seen before any of its files are classified.
func (t *scanTracker) noteDir(dir string) {
	t.updateSeq++
	t.ensureDir(dir).UpdatedSeq = t.updateSeq
}

// addEntry records one classified file into the result, the recent list, and
// its directory's rollup stats.
func (t *scanTracker) addEntry(entry ScanEntry) {
	t.result.Entries = append(t.result.Entries, entry)
	t.recent = append(t.recent, entry)

	t.updateSeq++
	dir := t.ensureDir(parentDir(entry.RelPath))
	dir.Files++
	dir.UpdatedSeq = t.updateSeq
	switch entry.Action {
	case ActionCopy:
		t.result.CopyCount++
		t.result.TotalBytes += entry.Size
		dir.CopyCount++
		dir.CopyBytes += entry.Size
	case ActionSkipIdentical:
		t.result.SkipCount++
		dir.SkipCount++
	case ActionConflict:
		t.result.ConflictCount++
		dir.ConflictCount++
	}
}

func (t *scanTracker) snapshotDirs() []ScanDirProgress {
	dirs := make([]ScanDirProgress, 0, len(t.dirStats))
	for _, d := range t.dirStats {
		dirs = append(dirs, *d)
	}
	// Stable path ordering so the folder list doesn't reshuffle as the
	// scan progresses (the user needs to click folders mid-scan).
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].Path < dirs[j].Path
	})
	return dirs
}

func (t *scanTracker) emit(currentDir, currentPath string, force bool) {
	if currentPath != "" {
		debugf("scan %s: checking %s", t.label, currentPath)
	}
	if t.progress == nil {
		return
	}
	now := time.Now()
	if !force && !t.lastEmit.IsZero() && now.Sub(t.lastEmit) < 100*time.Millisecond {
		return
	}
	t.lastEmit = now
	t.progress(ScanProgress{
		Label:         t.label,
		CurrentDir:    displayDir(currentDir),
		CurrentPath:   currentPath,
		FilesScanned:  t.result.CopyCount + t.result.SkipCount + t.result.ConflictCount,
		DirsSeen:      t.dirsSeen,
		CopyCount:     t.result.CopyCount,
		SkipCount:     t.result.SkipCount,
		ConflictCount: t.result.ConflictCount,
		TotalBytes:    t.result.TotalBytes,
		Recent:        append([]ScanEntry(nil), t.recent...),
		Dirs:          t.snapshotDirs(),
	})
}

// finish emits the final Done snapshot and returns the accumulated result.
func (t *scanTracker) finish() ScanResult {
	debugf("scan %s: done, %d to copy, %d identical, %d conflicts",
		t.label, t.result.CopyCount, t.result.SkipCount, t.result.ConflictCount)
	if t.progress != nil {
		t.progress(ScanProgress{
			Label:         t.label,
			FilesScanned:  t.result.CopyCount + t.result.SkipCount + t.result.ConflictCount,
			DirsSeen:      t.dirsSeen,
			CopyCount:     t.result.CopyCount,
			SkipCount:     t.result.SkipCount,
			ConflictCount: t.result.ConflictCount,
			TotalBytes:    t.result.TotalBytes,
			Recent:        append([]ScanEntry(nil), t.recent...),
			Dirs:          t.snapshotDirs(),
			Done:          true,
		})
	}
	return t.result
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

	tracker := newScanTracker(label, progress)

	for _, dir := range listing.dirs {
		tracker.noteDir(dir)
		tracker.emit(dir, dir, false)
	}

	for _, srcObj := range listing.objects {
		if err := ctx.Err(); err != nil {
			return ScanResult{}, err
		}
		entry, err := classifyObject(ctx, dstObjs, srcObj)
		if err != nil {
			return ScanResult{}, err
		}
		tracker.addEntry(entry)
		tracker.emit(parentDir(srcObj.Remote()), srcObj.Remote(), false)
	}

	return tracker.finish(), nil
}

// classifyObject decides what a copy would do with srcObj: copy it fresh, skip
// it as identical, or flag it as a conflict needing user resolution. See
// compareObjects for the size+prefix comparison used when a same-path file
// already exists at the destination.
func classifyObject(ctx context.Context, dstObjs map[string]fs.Object, srcObj fs.Object) (ScanEntry, error) {
	relFile := srcObj.Remote()
	entry := ScanEntry{RelPath: relFile, Size: srcObj.Size(), Action: ActionCopy}

	if dstObj, ok := dstObjs[relFile]; ok {
		action, reason, err := compareObjects(ctx, srcObj, dstObj)
		if err != nil {
			return ScanEntry{}, err
		}
		entry.Action = action
		if action == ActionConflict {
			entry.DstSize = dstObj.Size()
			entry.ConflictReason = reason
		}
	}
	return entry, nil
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
