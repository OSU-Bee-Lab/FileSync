package syncengine

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
)

// ExperimentEntry is one experiment directory found directly under a
// Location's experiments/ root — the only thing the Sync Experiments flow ever
// browses.
type ExperimentEntry struct {
	Name string
}

// ListExperiments lists exactly the top-level directories under <loc>/ — a
// single shallow List call, never recursive. This is the perf-critical fix
// motivating the whole tool: Sync Experiments never has to look at anything
// below this level to populate its picker.
func ListExperiments(ctx context.Context, loc Location) ([]ExperimentEntry, error) {
	entries, err := listDir(ctx, loc.rcloneSpec())
	if err != nil {
		return nil, err
	}
	out := make([]ExperimentEntry, 0, len(entries))
	for _, e := range entries {
		if _, isDir := e.(fs.Directory); isDir {
			out = append(out, ExperimentEntry{Name: dirName(e)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Entry is one child (file or directory) found while drilling into a
// Location's tree for the Pull Files flow.
type Entry struct {
	Name  string
	IsDir bool
	Size  int64 // 0 for directories
}

// ListChildren lists exactly one level under <loc>/<relPath>. relPath == ""
// lists the experiment directories themselves. It never recurses further
// than the requested level — the Pull Files flow's UI drills deeper by
// calling this again with the child's relPath appended.
func ListChildren(ctx context.Context, loc Location, relPath string) ([]Entry, error) {
	entries, err := listDir(ctx, joinSpec(loc.rcloneSpec(), relPath))
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		switch v := e.(type) {
		case fs.Directory:
			out = append(out, Entry{Name: dirName(e), IsDir: true})
		case fs.Object:
			out = append(out, Entry{Name: dirName(e), IsDir: false, Size: v.Size()})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir // directories first
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ListChildrenUnion is ListChildren, but across every one of locs at once:
// it lists relPath at each location concurrently and returns the
// deduped/sorted union of whatever children exist, so Manage Files can show
// every folder present on any selected Location instead of gating on a
// single reference one. A path only has to exist on one Location to show up
// - Manage Files operations already tolerate per-Location misses (see
// ApplyRenames), so a folder missing from some Locations is normal, not an
// error.
//
// notFound reports whether relPath named an existing directory on none of
// the locations that could be listed at all - used to show a "(new
// folder)" note. isFile reports whether relPath named a file (not a
// directory) on at least one listable location. err is returned only when
// every location failed with something other than "not found"/"is a
// file" (e.g. every remote's token expired) - if even one location listed
// successfully, the others' hard errors are swallowed the same as a
// Location simply not having this folder.
// presence is one entry's Location-by-Location membership, in the same
// order the caller's locs slice was given - one bool per Location, true if
// that Location's listing contained the entry. Shared by ListChildrenUnion
// and the Union*Stream listers below so a presence-indicator UI can show
// exactly which Locations hold a given file/folder alongside the union
// listing they already have to build.
type presence = map[string][]bool

// unionEntries merges one folder's per-Location listings (perLoc, index-
// aligned with locs) into a single deduped, sorted union plus each entry's
// per-Location presence. It is the one place that merge happens, shared by
// ListChildrenUnion and UnionChildEntriesStream.
//
// A Results Location's "<stem>_buzzdetect.csv" is folded onto the recording
// it mirrors whenever some non-Results Location in the same listing holds
// that recording, so a mixed Audio+Results selection shows one row per
// recording whose presence dots cover both trees - rather than the
// recording and its result as two unrelated rows. That also matches what
// an operation on that row actually does: Manage Files resolves an audio
// path to its counterpart at each Results Location (see resolveResultsLeaf).
// A result with no counterpart in this listing (a Results-only selection,
// or an orphaned result) keeps its own row, so nothing is ever hidden.
func unionEntries(locs []Location, perLoc [][]Entry) ([]Entry, presence) {
	seen := make(map[string]Entry)
	pres := make(presence)
	// byStem indexes the audio side by filename stem, so a result can find
	// the recording it belongs to (its own name can't say which extension
	// that recording had). First one wins, so the merge is deterministic
	// even in the odd case of "rec.mp3" and "rec.wma" side by side.
	byStem := make(map[string]string)

	add := func(name string, e Entry, i int) {
		if existing, ok := seen[name]; !ok || (!existing.IsDir && e.IsDir) {
			seen[name] = e
		}
		if pres[name] == nil {
			pres[name] = make([]bool, len(locs))
		}
		pres[name][i] = true
	}

	// Pass 1: every non-Results Location, keyed by the real entry name.
	for i, entries := range perLoc {
		if i >= len(locs) || locs[i].Role == RoleResults {
			continue
		}
		for _, e := range entries {
			add(e.Name, e, i)
			if !e.IsDir {
				if stem := strings.TrimSuffix(e.Name, path.Ext(e.Name)); byStem[stem] == "" {
					byStem[stem] = e.Name
				}
			}
		}
	}

	// Pass 2: Results Locations, folded onto pass 1's recordings where they
	// have a counterpart there. Directories mirror one-to-one and so are
	// never folded.
	for i, entries := range perLoc {
		if i >= len(locs) || locs[i].Role != RoleResults {
			continue
		}
		for _, e := range entries {
			name := e.Name
			if !e.IsDir {
				if stem, ok := strings.CutSuffix(name, resultsFileSuffix); ok {
					if audio := byStem[stem]; audio != "" {
						name = audio
					}
				}
			}
			add(name, e, i)
		}
	}

	out := make([]Entry, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, pres
}

func ListChildrenUnion(ctx context.Context, locs []Location, relPath string) (entries []Entry, notFound bool, isFile bool, pres presence, err error) {
	return ListChildrenUnionStream(ctx, locs, relPath, nil)
}

// streamLocations is the one place that fans a listing out across locs
// concurrently: it calls list(i, loc) for every Location in its own
// goroutine, then - holding a shared lock, so callers never see two calls
// overlap - marks that Location loaded and invokes fold with its result (or
// error) plus a fresh snapshot of which Locations have reported so far.
// loaded is index-aligned with locs and safe for fold to hand onward (it's
// cloned per call, never the live backing array). Shared by every
// Union*Stream/ListChildrenUnionStream lister below, all of which differ
// only in what they list and how they fold a result into the running union
// - not in the fan-out/loaded-tracking mechanics, which used to be
// copy-pasted three times.
func streamLocations(locs []Location, list func(i int, loc Location) ([]Entry, error), fold func(i int, entries []Entry, err error, loaded []bool)) {
	loaded := make([]bool, len(locs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, loc := range locs {
		wg.Add(1)
		go func(i int, loc Location) {
			defer wg.Done()
			entries, err := list(i, loc)
			mu.Lock()
			defer mu.Unlock()
			loaded[i] = true
			fold(i, entries, err, cloneLoaded(loaded))
		}(i, loc)
	}
	wg.Wait()
}

func cloneLoaded(loaded []bool) []bool {
	out := make([]bool, len(loaded))
	copy(out, loaded)
	return out
}

// ListChildrenUnionStream is ListChildrenUnion, but invokes onUpdate with the
// union built so far each time a Location's listing lands, instead of only
// handing back one result once the slowest Location has answered. This lets
// Manage Files paint a fast Location's children (local disk) immediately
// rather than blocking the whole browser on a slow remote. onUpdate is only
// ever called from one goroutine at a time, and never with a union that a
// still-running listing could mutate (unionEntries builds fresh values).
// loaded is index-aligned with locs, true once that Location has reported
// (successfully or not) - see streamLocations. The return values are exactly
// ListChildrenUnion's, after every Location has finished. onUpdate may be
// nil.
func ListChildrenUnionStream(ctx context.Context, locs []Location, relPath string, onUpdate func(entries []Entry, pres presence, loaded []bool)) (entries []Entry, notFound bool, isFile bool, pres presence, err error) {
	perLoc := make([][]Entry, len(locs))
	anyOK := false
	anyIsFile := false
	var firstHardErr error

	streamLocations(locs,
		func(i int, loc Location) ([]Entry, error) { return ListChildren(ctx, loc, relPath) },
		func(i int, e []Entry, lerr error, loaded []bool) {
			if lerr != nil {
				switch {
				case errors.Is(lerr, fs.ErrorIsFile):
					anyIsFile = true
				case errors.Is(lerr, fs.ErrorDirNotFound):
					// Not present at this Location - fine, others may have it.
				default:
					if firstHardErr == nil {
						firstHardErr = lerr
					}
				}
			} else {
				anyOK = true
				perLoc[i] = e
			}
			if onUpdate != nil {
				out, p := unionEntries(locs, perLoc)
				onUpdate(out, p, loaded)
			}
		})

	if !anyOK && firstHardErr != nil {
		return nil, false, false, nil, firstHardErr
	}

	out, pres := unionEntries(locs, perLoc)
	return out, !anyOK && !anyIsFile, anyIsFile, pres, nil
}

// ListRemoteDirsOnDrive lists only the sub-directories (not files) directly
// under a remote path, one shallow level, browsing a specific drive of the
// remote by overriding its saved drive_id/drive_type for this listing only via
// an rclone connection string (remote,drive_id=..,drive_type=..:path). It backs
// the wizard/edit "browse the remote" folder picker (which drills deeper by
// calling again with the chosen child appended) and lets the setup browser show
// a document library's contents before that drive has been committed to the
// remote's config. A zero DriveInfo (empty ID) falls back to the remote's own
// configured drive. relPath == "" lists the remote's root.
func ListRemoteDirsOnDrive(ctx context.Context, remoteName string, d DriveInfo, relPath string) ([]string, error) {
	spec := remoteName
	if d.ID != "" {
		spec += fmt.Sprintf(",drive_id=%s,drive_type=%s", d.ID, d.Type)
	}
	return listDirNames(ctx, spec+":"+relPath)
}

// listDirNames lists just the sub-directory names one shallow level under an
// rclone spec, sorted. Shared by the remote folder-browser entry points.
func listDirNames(ctx context.Context, spec string) ([]string, error) {
	entries, err := listDir(ctx, spec)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if _, isDir := e.(fs.Directory); isDir {
			out = append(out, dirName(e))
		}
	}
	sort.Strings(out)
	return out, nil
}

// listDir resolves spec to an fs.Fs (via the backend cache, so repeated
// browsing of the same root reuses the connection instead of
// re-authenticating every call) and lists its root, i.e. one shallow List.
func listDir(ctx context.Context, spec string) (fs.DirEntries, error) {
	f, err := cache.Get(ctx, spec)
	if err != nil {
		return nil, err
	}
	return f.List(ctx, "")
}

// UnionChildDirNames lists the sub-directory names one shallow level under
// relPath, across every one of locs, and returns the deduped/sorted union.
// It backs the recorder-sync folder browser, whose destination is a set of
// locations at once rather than a single source - a folder only has to
// exist on one of them to show up as navigable. Locations that fail to
// list (e.g. an unreachable remote) are silently skipped rather than
// aborting the whole listing, matching the tolerance ListExperiments'
// callers already relied on.
func UnionChildDirNames(ctx context.Context, locs []Location, relPath string) []string {
	seen := make(map[string]bool)
	for _, loc := range locs {
		entries, err := listDir(ctx, joinSpec(loc.rcloneSpec(), relPath))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if _, isDir := e.(fs.Directory); isDir {
				seen[dirName(e)] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// UnionChildEntriesStream scans every one of locs concurrently for relPath's
// immediate children (files and directories, each tagged via Entry.IsDir),
// invoking onUpdate with the current deduped/sorted union - and each entry's
// per-Location presence - every time a Location's listing lands, instead of
// waiting for all of them. This lets a caller show a fast Location's
// contents (e.g. local disk) immediately rather than blocking on a slow one
// (e.g. a remote). It backs the "Edit Sync Locations" browse dialog, where
// seeing the files already at a candidate path helps confirm it's the right
// one before adopting it as the Location's root. Locations that fail to
// list (e.g. an unreachable remote) are silently skipped, same as
// ListChildrenUnion's tolerance, but still marked loaded via streamLocations
// - a failure is still an answer, not a still-pending Location.
func UnionChildEntriesStream(ctx context.Context, locs []Location, relPath string, onUpdate func(entries []Entry, pres presence, loaded []bool)) {
	if len(locs) == 0 {
		return
	}
	// Each location's own listing is kept as it lands and the union is
	// rebuilt from all of them on every update (unionEntries), rather than
	// accumulated in place: folding a result onto its recording depends on
	// which Locations have reported so far, so a Results Location that
	// happens to answer first must still fold once the Audio Location it
	// mirrors arrives.
	perLoc := make([][]Entry, len(locs))
	streamLocations(locs,
		func(i int, loc Location) ([]Entry, error) { return ListChildren(ctx, loc, relPath) },
		func(i int, e []Entry, err error, loaded []bool) {
			if err == nil {
				perLoc[i] = e
			}
			out, pres := unionEntries(locs, perLoc)
			onUpdate(out, pres, loaded)
		})
}

// UnionChildDirNamesStream is UnionChildEntriesStream, but returns just the
// deduped/sorted directory names, filtering out files. It backs the
// recorder-sync folder browser, whose destination is a set of locations at
// once rather than a single source - a folder only has to exist on one of
// them to show up as navigable.
func UnionChildDirNamesStream(ctx context.Context, locs []Location, relPath string, onUpdate func(names []string, pres presence, loaded []bool)) {
	UnionChildEntriesStream(ctx, locs, relPath, func(entries []Entry, pres presence, loaded []bool) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir {
				names = append(names, e.Name)
			}
		}
		onUpdate(names, pres, loaded)
	})
}

func dirName(e fs.DirEntry) string {
	return path.Base(e.Remote())
}
