package syncengine

import (
	"context"
	"fmt"
	"path"
	"sort"

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

// ListRemoteDirs lists only the sub-directories (not files) directly under
// <remoteName>:<relPath>, one shallow level. It backs the wizard/edit
// "browse the remote" folder picker, which drills deeper by calling again
// with the chosen child appended. relPath == "" lists the remote's root.
func ListRemoteDirs(ctx context.Context, remoteName, relPath string) ([]string, error) {
	return listDirNames(ctx, remoteName+":"+relPath)
}

// ListRemoteDirsOnDrive is like ListRemoteDirs but browses a specific drive
// of the remote, overriding its saved drive_id/drive_type for this listing
// only via an rclone connection string (remote,drive_id=..,drive_type=..:path).
// This lets the setup browser show a document library's contents before that
// drive has been committed to the remote's config. A zero DriveInfo (empty
// ID) falls back to the remote's own configured drive.
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

func dirName(e fs.DirEntry) string {
	return path.Base(e.Remote())
}
