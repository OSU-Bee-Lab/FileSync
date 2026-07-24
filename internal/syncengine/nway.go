package syncengine

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
)

// FileConvergenceStatus classifies one relative path's state across every
// participating location. See .local/NWAY.md for the design this
// implements — the one deliberate deviation from that doc is the
// same-vs-different test: NWAY.md originally specified size-only equality
// (matching the pairwise scan's behavior at the time it was written), but
// this repo has since established (see NOTES.md) that size alone can't
// distinguish these recorders' files — rollover hits an exact byte-for-byte
// size cap, so two genuinely different recordings routinely share a size.
// N-way comparison uses the same size+256KB-prefix check
// (compareObjects/compareObjectsN) as pairwise sync.
type FileConvergenceStatus int

const (
	// FileInSync: present at every participating location, and every
	// present copy agrees (size + leading bytes) — nothing to do.
	FileInSync FileConvergenceStatus = iota
	// FileMissingSome: present at 1..N-1 locations, and every location
	// that has it agrees with every other. Unambiguous: copy from any
	// location that has it to every location missing it.
	FileMissingSome
	// FileConflict: two or more locations have the file but disagree on
	// size and/or leading bytes. Never auto-resolved — needs an explicit
	// per-file decision from the user, same as the pairwise ActionConflict.
	// Only reachable under NWayFullScan — NWayQuickScan never reads bytes,
	// so it can never detect (or produce) a conflict.
	FileConflict
)

// NWayScanMode controls whether diffNWay reads file bytes for paths that
// collide across locations.
type NWayScanMode int

const (
	// NWayFullScan compares present copies of every colliding path via
	// compareObjectsN (size + 256KB prefix), same as always. Can produce
	// FileConflict.
	NWayFullScan NWayScanMode = iota
	// NWayQuickScan never reads file bytes: a path present everywhere is
	// FileInSync, a path missing anywhere is FileMissingSome, regardless of
	// whether present copies actually agree. Existence-only — meant for
	// the common case of topping up a handful of new files into a large
	// already-converged experiment without paying for a byte comparison on
	// every already-present file. Never produces FileConflict.
	NWayQuickScan
)

// FileLocationState is one location's view of one relative path.
type FileLocationState struct {
	Location Location
	Exists   bool
	Size     int64
	// ModTime is this copy's modification time as reported by the backend's
	// listing (no extra round trip). Shown in the conflict resolver so the
	// user can tell e.g. a stale partial upload from the fresh original —
	// size alone can't distinguish them.
	ModTime time.Time
	object  fs.Object // nil unless Exists; kept only for building a transfer plan
}

// FileConvergencePlan is the full picture for one relative path across all
// N compared locations.
type FileConvergencePlan struct {
	RelPath string
	States  []FileLocationState // one per input location, same order as passed to ScanNWay
	Status  FileConvergenceStatus
	// ConflictReason is only set when Status == FileConflict — a short
	// human-readable explanation (see compareObjectsN), for display in a
	// conflict-resolution prompt.
	ConflictReason string
}

// NWayScanResult is the N-way analog of ScanResult: the full comparison
// across every participating location for one relPath subtree (typically
// one experiment), before any copies run.
type NWayScanResult struct {
	Locations        []Location
	Files            []FileConvergencePlan
	InSyncCount      int
	MissingSomeCount int
	ConflictCount    int
}

// ScanNWay lists relPath under every one of locs (in parallel — these are
// independent network calls, same pattern as this package's existing
// maxConcurrentTasks-bounded scan/copy tasks) and classifies every file
// found at any of them. This never transfers anything — see
// BuildNWayTransferPlan for turning the result into copy jobs.
//
// Every listing error is returned, not swallowed: a location that fails to
// list must never be silently treated as "has none of the files," which
// would misclassify every file that location actually has as missing and
// queue redundant/wrong-direction copies.
func ScanNWay(ctx context.Context, locs []Location, relPath string, fset FilterSettings, mode NWayScanMode) (NWayScanResult, error) {
	return ScanNWayWithProgress(ctx, locs, relPath, fset, nil, mode)
}

// ScanNWayWithProgress is ScanNWay with live progress updates, in the same
// ScanProgress shape the pairwise scan emits so the shared scan/sync screen
// renders an N-way scan identically: aggregate file/dir counts while the
// per-location listings run, then per-file entries (mapped through
// nwayDisplayEntry) with directory rollups as the diff classifies each path.
func ScanNWayWithProgress(ctx context.Context, locs []Location, relPath string, fset FilterSettings, progress ScanProgressFunc, mode NWayScanMode) (NWayScanResult, error) {
	return ScanNWayScopedWithProgress(ctx, locs, relPath, nil, fset, progress, mode)
}

// ScanNWayScopedWithProgress is ScanNWayWithProgress restricted to a set of
// subtrees of relPath. Every scope is a path relative to relPath; only those
// subtrees are walked at each location, but every reported RelPath stays
// relative to relPath itself, so the result is indistinguishable from a full
// scan that happened to find nothing outside the scopes. A nil or empty
// scopes walks the whole of relPath (what ScanNWayWithProgress does).
//
// This exists for callers that already know exactly which files they care
// about and would otherwise pay to walk an entire experiment to find them —
// batch upload after a recorder offload being the motivating case: it holds
// the exact set of files this session wrote (see runBatchUploadScan) and
// discards everything else from the scan result anyway, so walking every
// prior deployment at every location was pure cost. Use CoveringDirs to turn
// a known set of file paths into the minimal scopes covering them.
//
// Scopes are walked concurrently across every (location, scope) pair, and a
// scope missing at a location is benign-empty (same rule as a missing
// relPath — see listSource), so a location that lacks the subtree entirely
// costs one failed listing rather than one lookup per file.
func ScanNWayScopedWithProgress(ctx context.Context, locs []Location, relPath string, scopes []string, fset FilterSettings, progress ScanProgressFunc, mode NWayScanMode) (NWayScanResult, error) {
	if len(locs) < 2 {
		return NWayScanResult{}, fmt.Errorf("nway scan needs at least 2 locations, got %d", len(locs))
	}
	if len(scopes) == 0 {
		scopes = []string{""}
	}

	// One listing job per (location, scope). They run in parallel, so their
	// progress callbacks are folded into one aggregate count under a shared
	// throttle.
	type listJob struct {
		loc   int
		scope string
	}
	var jobs []listJob
	for i := range locs {
		for _, scope := range scopes {
			jobs = append(jobs, listJob{loc: i, scope: scope})
		}
	}

	var mu sync.Mutex
	filesSeen := make([]int, len(jobs))
	dirsSeen := make([]int, len(jobs))
	var lastEmit time.Time
	listProgress := func(j int) ScanProgressFunc {
		if progress == nil {
			return nil
		}
		return func(p ScanProgress) {
			mu.Lock()
			filesSeen[j] = p.FilesScanned
			dirsSeen[j] = p.DirsSeen
			now := time.Now()
			if !lastEmit.IsZero() && now.Sub(lastEmit) < 100*time.Millisecond {
				mu.Unlock()
				return
			}
			lastEmit = now
			var files, dirs int
			for k := range filesSeen {
				files += filesSeen[k]
				dirs += dirsSeen[k]
			}
			mu.Unlock()
			progress(ScanProgress{Label: relPath, FilesScanned: files, DirsSeen: dirs})
		}
	}

	partials := make([]SourceListing, len(jobs))
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	for j, job := range jobs {
		wg.Add(1)
		go func(j int, job listJob) {
			defer wg.Done()
			loc := locs[job.loc]
			partials[j], errs[j] = listSource(ctx, loc.rcloneSpec(), path.Join(relPath, job.scope), loc.reachAnchor, job.scope, fset, listProgress(j))
		}(j, job)
	}
	wg.Wait()

	listings := make([]SourceListing, len(locs))
	for j, err := range errs {
		if err != nil {
			return NWayScanResult{}, fmt.Errorf("listing %s: %w", locs[jobs[j].loc].Name, err)
		}
		listings[jobs[j].loc].merge(partials[j])
	}

	return diffNWay(ctx, locs, listings, relPath, progress, mode)
}

// maxScanScopes bounds how many subtrees CoveringDirs will hand back, and so
// (times the number of locations) how many concurrent listings a scoped scan
// fans out into. Measured against a real SharePoint remote, scope *count*
// costs remarkably little once the listings run concurrently — 16 scopes ran
// ~2.0s against ~1.5s for one — because the dominant per-scope cost is
// constructing the rclone Fs, and those overlap. What the cap is really for
// is the tail: an experiment laid out with hundreds of leaf directories
// would otherwise fan out into hundreds of simultaneous listings and get
// throttled, losing far more than the narrowing ever won. Eight keeps the
// common shapes exact (a handful of recorder directories) while collapsing
// anything pathological up to a shallower ancestor, which costs only the
// extra breadth of one listing.
const maxScanScopes = 8

// CoveringDirs returns a small set of directories that together contain every
// path in relPaths, suitable as ScanNWayScopedWithProgress scopes: the parent
// of each path, with any directory that already sits under another in the set
// dropped, then lifted toward the root while more than maxScanScopes remain.
//
// A nil return means "no useful narrowing" — relPaths was empty, at least one
// path sits directly at the root, or lifting ran all the way up — and the
// caller should walk the whole root.
func CoveringDirs(relPaths []string) []string {
	seen := make(map[string]bool, len(relPaths))
	for _, p := range relPaths {
		dir := parentDir(path.Clean(p))
		if dir == "" {
			return nil
		}
		seen[dir] = true
	}
	if len(seen) == 0 {
		return nil
	}

	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	dirs = minimalDirs(dirs)
	for len(dirs) > maxScanScopes {
		if dirs = liftDeepest(dirs); dirs == nil {
			return nil
		}
	}
	return dirs
}

// minimalDirs drops every directory that duplicates or already sits under
// another one in dirs, returning the rest sorted. Deduplicating matters as
// much as the ancestor test: liftDeepest lifts whole levels at once, so
// merging siblings only actually shrinks the set once their now-identical
// parents collapse into one.
func minimalDirs(dirs []string) []string {
	// Sorted ascending, an ancestor always precedes its descendants
	// ("a/b" < "a/b/c"), so one pass keeping only paths not already covered
	// by something kept leaves exactly the minimal set.
	sort.Strings(dirs)
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		covered := false
		for _, keep := range out {
			if d == keep || strings.HasPrefix(d, keep+"/") {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, d)
		}
	}
	return out
}

// liftDeepest replaces every deepest directory in dirs with its parent and
// re-minimizes, shrinking the set by merging siblings. Returns nil once that
// would lift something to the root (at which point only a whole-root walk
// covers it). Each call strictly reduces the maximum depth, so repeated
// calls terminate.
func liftDeepest(dirs []string) []string {
	depth := func(d string) int { return strings.Count(d, "/") }
	maxDepth := 0
	for _, d := range dirs {
		if n := depth(d); n > maxDepth {
			maxDepth = n
		}
	}

	lifted := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if depth(d) < maxDepth {
			lifted = append(lifted, d)
			continue
		}
		parent := parentDir(d)
		if parent == "" {
			return nil
		}
		lifted = append(lifted, parent)
	}
	return minimalDirs(lifted)
}

// diffNWay computes, for every relative path seen at any of the listings,
// which locations have it and (under NWayFullScan) whether every present
// copy agrees.
func diffNWay(ctx context.Context, locs []Location, listings []SourceListing, label string, progress ScanProgressFunc, mode NWayScanMode) (NWayScanResult, error) {
	perLoc := make([]map[string]fs.Object, len(locs))
	for i, listing := range listings {
		m := make(map[string]fs.Object, len(listing.objects))
		for _, obj := range listing.objects {
			m[obj.rel] = obj.obj
		}
		perLoc[i] = m
	}

	seen := make(map[string]bool)
	var order []string
	for _, m := range perLoc {
		for relPath := range m {
			if !seen[relPath] {
				seen[relPath] = true
				order = append(order, relPath)
			}
		}
	}
	sort.Strings(order)

	tracker := newScanTracker(label, progress)

	// Pre-register every directory seen at any location, same as the
	// pairwise path (scanAgainstDest ranges over listing.dirs before
	// classifying any file) — otherwise a directory with no files that
	// differ (e.g. one that's entirely empty, or entirely in sync) never
	// gets an addEntry call and silently vanishes from the live scan UI's
	// folder list, which would also otherwise only grow incrementally as
	// files are classified rather than appearing upfront.
	seenDirs := make(map[string]bool)
	for _, listing := range listings {
		for _, dir := range listing.dirs {
			if seenDirs[dir] {
				continue
			}
			seenDirs[dir] = true
			tracker.noteDir(dir)
			tracker.emit(dir, dir, false)
		}
	}

	result := NWayScanResult{Locations: locs}
	for _, relPath := range order {
		if err := ctx.Err(); err != nil {
			return NWayScanResult{}, err
		}
		states := make([]FileLocationState, len(locs))
		var presentObjs []fs.Object
		presentCount := 0
		for i, loc := range locs {
			if obj, ok := perLoc[i][relPath]; ok {
				states[i] = FileLocationState{Location: loc, Exists: true, Size: obj.Size(), ModTime: obj.ModTime(ctx), object: obj}
				presentObjs = append(presentObjs, obj)
				presentCount++
			} else {
				states[i] = FileLocationState{Location: loc}
			}
		}

		plan := FileConvergencePlan{RelPath: relPath, States: states}
		if presentCount >= 2 && mode == NWayFullScan {
			identical, reason, err := compareObjectsN(ctx, presentObjs)
			if err != nil {
				return NWayScanResult{}, err
			}
			if !identical {
				plan.Status = FileConflict
				plan.ConflictReason = reason
			}
		}
		if plan.Status != FileConflict {
			if presentCount < len(locs) {
				plan.Status = FileMissingSome
			} else {
				plan.Status = FileInSync
			}
		}

		switch plan.Status {
		case FileInSync:
			result.InSyncCount++
		case FileMissingSome:
			result.MissingSomeCount++
		case FileConflict:
			result.ConflictCount++
		}
		result.Files = append(result.Files, plan)

		tracker.addEntry(nwayDisplayEntry(plan))
		tracker.emit(parentDir(relPath), relPath, false)
	}
	tracker.finish()

	return result, nil
}

// nwayDisplayEntry maps one N-way convergence plan onto the pairwise
// ScanEntry shape, purely for display in the shared scan/sync screen:
// in-sync files render as already-synced, missing-some as pending copies,
// conflicts as conflicts. Size is one present copy's size (the content that
// must converge), not size × number of destination legs.
func nwayDisplayEntry(plan FileConvergencePlan) ScanEntry {
	entry := ScanEntry{RelPath: plan.RelPath}
	for _, st := range plan.States {
		if st.Exists {
			entry.Size = st.Size
			break
		}
	}
	switch plan.Status {
	case FileInSync:
		entry.Action = ActionSkipIdentical
	case FileMissingSome:
		entry.Action = ActionCopy
	case FileConflict:
		entry.Action = ActionConflict
		entry.ConflictReason = plan.ConflictReason
	}
	return entry
}

// NWayDisplayScanResult renders a whole NWayScanResult in the pairwise
// ScanResult shape (see nwayDisplayEntry) so the shared scan/sync screen can
// show an N-way scan's outcome with the exact same lists, splits, and counts
// as a pairwise scan.
func NWayDisplayScanResult(result NWayScanResult) ScanResult {
	tracker := newScanTracker("", nil)
	for _, plan := range result.Files {
		tracker.addEntry(nwayDisplayEntry(plan))
	}
	return tracker.result
}

// NWayTransfer is one planned copy: RelPath from Source to Dest.
type NWayTransfer struct {
	Source  Location
	Dest    Location
	RelPath string
	Size    int64
}

// NWayTransferPair is every file that needs to move from Source to Dest, one
// group per (source, dest) pair so it can be driven by a single
// StartSyncExperiments-style job instead of one job per file.
type NWayTransferPair struct {
	Source Location
	Dest   Location
	Files  []NWayTransfer
}

// BuildNWayTransferPlan turns a scanned NWayScanResult into the minimal set
// of copies needed to converge every FileMissingSome path, grouped by
// (source, dest) pair in first-seen order for a stable job ordering. For a
// path missing from k of N locations, this produces exactly k copies (a
// fan-out from one source), never a k*(k-1) full cross product.
// FileConflict paths are never included — they're never auto-resolved.
//
// preferSource, if non-nil, is consulted to break ties when more than one
// location has a missing file: given the two candidate locations currently
// considered (best-so-far, candidate), it should return true if candidate
// should replace best-so-far as the copy source. If nil, the first location
// (in the order passed to ScanNWay) that has the file is always used —
// e.g. pass a preferSource that prefers LocationLocal over LocationRemote
// to avoid slow upload legs when a local copy is available.
func BuildNWayTransferPlan(result NWayScanResult, preferSource func(bestSoFar, candidate Location) bool) []NWayTransferPair {
	type pairKey struct{ src, dst string }
	pairIndex := make(map[pairKey]int)
	var pairs []NWayTransferPair

	for _, plan := range result.Files {
		if plan.Status != FileMissingSome {
			continue
		}

		srcIdx := -1
		for i, st := range plan.States {
			if !st.Exists {
				continue
			}
			if srcIdx == -1 {
				srcIdx = i
				continue
			}
			if preferSource != nil && preferSource(plan.States[srcIdx].Location, st.Location) {
				srcIdx = i
			}
		}
		if srcIdx == -1 {
			continue // unreachable: FileMissingSome implies at least one present
		}
		src := plan.States[srcIdx]

		for i, st := range plan.States {
			if st.Exists {
				continue
			}
			key := pairKey{src.Location.ID, plan.States[i].Location.ID}
			idx, ok := pairIndex[key]
			if !ok {
				idx = len(pairs)
				pairIndex[key] = idx
				pairs = append(pairs, NWayTransferPair{Source: src.Location, Dest: plan.States[i].Location})
			}
			pairs[idx].Files = append(pairs[idx].Files, NWayTransfer{
				Source:  src.Location,
				Dest:    plan.States[i].Location,
				RelPath: plan.RelPath,
				Size:    src.Size,
			})
		}
	}

	return pairs
}

// FilterNWayToSourcePresent returns a copy of result keeping only files that
// exist at the location whose ID is srcID, with the summary counts recomputed
// to match. It's what makes One-Way Sync a strict push of the source folder's
// own contents: a path present only at a destination (never at the source) is
// dropped entirely, so it's never propagated between destinations, flagged as
// a conflict the user has to resolve, or touched by a rename/delete
// resolution. Files the source has are kept verbatim (same States, same
// Status), so an unresolved source-vs-destination conflict still surfaces.
func FilterNWayToSourcePresent(result NWayScanResult, srcID string) NWayScanResult {
	out := result
	out.Files = nil
	out.InSyncCount, out.MissingSomeCount, out.ConflictCount = 0, 0, 0
	for _, f := range result.Files {
		srcPresent := false
		for _, st := range f.States {
			if st.Location.ID == srcID && st.Exists {
				srcPresent = true
				break
			}
		}
		if !srcPresent {
			continue
		}
		out.Files = append(out.Files, f)
		switch f.Status {
		case FileInSync:
			out.InSyncCount++
		case FileMissingSome:
			out.MissingSomeCount++
		case FileConflict:
			out.ConflictCount++
		}
	}
	return out
}

// ScanResultFromNWayTransfers converts one NWayTransferPair's files into a
// ScanResult shaped like a pairwise scan's output: every pair.Files entry is
// ActionCopy, so the existing StartSyncExperiments/filesFromFilter copy
// machinery (which only ever acts on ActionCopy entries) drives it verbatim —
// N-way sync's only novel contribution is the planning above (ScanNWay/
// BuildNWayTransferPlan); execution reuses the pairwise job code unchanged.
//
// scan is the NWayScanResult the pair was built from; every FileInSync path
// in it is present at every location scanned, including this pair's source
// and destination, so it's carried along as an inert ActionSkipIdentical
// entry. filesFromFilter and the copy engine ignore non-ActionCopy entries,
// so this is display-only: it's what lets the Ready-to-Sync/Syncing screens
// keep showing "Previously synced" instead of losing it once the transfer plan
// (copy-only by nature) replaces the full scan result as this task's data.
func ScanResultFromNWayTransfers(scan NWayScanResult, pair NWayTransferPair) ScanResult {
	result := ScanResult{
		Entries:   make([]ScanEntry, 0, len(pair.Files)+len(scan.Files)),
		CopyCount: len(pair.Files),
	}
	for _, f := range pair.Files {
		result.Entries = append(result.Entries, ScanEntry{RelPath: f.RelPath, Size: f.Size, Action: ActionCopy})
		result.TotalBytes += f.Size
	}
	for _, plan := range scan.Files {
		if plan.Status == FileInSync {
			result.Entries = append(result.Entries, nwayDisplayEntry(plan))
		}
	}
	return result
}

// PreferLocalSource is a BuildNWayTransferPlan preferSource callback that
// prefers a local location over a remote one as a copy source (cheaper/
// faster to read from than uploading out of a cloud remote), and otherwise
// keeps whichever candidate was found first.
func PreferLocalSource(bestSoFar, candidate Location) bool {
	return bestSoFar.Kind == LocationRemote && candidate.Kind == LocationLocal
}
