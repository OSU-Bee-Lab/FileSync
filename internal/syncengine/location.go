package syncengine

import (
	"encoding/json"
	"fmt"
	"path"
)

// LocationKind distinguishes a plain local filesystem root from a root
// backed by a configured rclone remote.
type LocationKind int

const (
	LocationLocal LocationKind = iota
	LocationRemote
)

func (k LocationKind) String() string {
	if k == LocationRemote {
		return "remote"
	}
	return "local"
}

// MarshalJSON/UnmarshalJSON render LocationKind as "local"/"remote" in
// FileSync's config.json rather than a bare int, so the file stays
// hand-readable and stable across any future reordering of the iota.
func (k LocationKind) MarshalJSON() ([]byte, error) {
	return json.Marshal(k.String())
}

func (k *LocationKind) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	switch s {
	case "local":
		*k = LocationLocal
	case "remote":
		*k = LocationRemote
	default:
		return fmt.Errorf("unknown location kind %q", s)
	}
	return nil
}

// LocationRole distinguishes a Location holding recorded audio from one
// holding buzzdetect results (a mirrored tree of per-file .csv outputs).
// Orthogonal to LocationKind — a Results location can be local or remote
// same as an Audio one.
type LocationRole int

const (
	RoleAudio LocationRole = iota // zero value - old config.json files load as all-Audio, no migration needed
	RoleResults
)

func (r LocationRole) String() string {
	if r == RoleResults {
		return "results"
	}
	return "audio"
}

func (r LocationRole) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.String())
}

func (r *LocationRole) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	switch s {
	case "audio":
		*r = RoleAudio
	case "results":
		*r = RoleResults
	default:
		return fmt.Errorf("unknown location role %q", s)
	}
	return nil
}

// Location is one storage root a collaborator has configured — a local
// drive/folder or a remote (SharePoint, Drive, Dropbox, S3, ...). Nothing
// about a Location is hardcoded: every field is user-supplied and
// persisted per-machine by internal/appconfig.
type Location struct {
	ID   string       `json:"id"`
	Name string       `json:"name"`
	Kind LocationKind `json:"kind"`

	// Role distinguishes recorded audio from buzzdetect results (see
	// LocationRole) - orthogonal to Kind.
	Role LocationRole `json:"role,omitempty"`

	// RemoteName is the rclone remote name (as stored in rclone's own
	// config file) backing this location. Empty when Kind == LocationLocal.
	RemoteName string `json:"remoteName,omitempty"`

	// RootPath is either an absolute local filesystem path (LocationLocal)
	// or a path within the remote (LocationRemote), e.g. "" or
	// "Bee Lab Docs". It points directly at the experiments/ root — the
	// folder whose immediate children are experiment directories.
	RootPath string `json:"rootPath"`

	// Priority ranks this location among other local locations as an N-way
	// sync source: 1 beats 2 beats 3, etc. It's only meaningful for
	// LocationLocal (see PreferLocalSource, which already always prefers any
	// local over any remote — Priority is the tie-break among locals, so the
	// fastest drive can be ranked ahead of a slower one). It's kept in sync
	// with slice order in appconfig.Config.Locations, which is what
	// BuildNWayTransferPlan actually iterates for its tie-break.
	Priority int `json:"priority,omitempty"`

	// SharePointSiteURL is the SharePoint site URL the user pasted in when
	// setting up this location, kept only so Edit Location can show it back
	// to them - rclone itself never persists it (see
	// syncengine.SharePointSiteURLKey), resolving straight to drive_id /
	// drive_type instead. Only meaningful for a OneDrive/SharePoint remote
	// location; empty for anything else, including OneDrive locations added
	// before this field existed.
	SharePointSiteURL string `json:"sharePointSiteURL,omitempty"`

	// reachAnchor is an rclone spec that must be reachable for a "directory
	// not found" at this location's own root to count as benign-empty (a
	// folder that simply hasn't been created yet) rather than a hard listing
	// error. It's only set by SubLocation, where the base Location's root is
	// the anchor and the folded-in sub-path is the leaf that may not exist
	// yet — the destination-side analogue of listSource's relPath != "" case.
	// Unexported so it's never persisted; meaningful only for the lifetime of
	// one scan session.
	reachAnchor string
}

// rcloneSpec returns the fs.NewFs-ready path string for this location, e.g.
// "/Volumes/BeeLabServer" or "sharepoint-osu:Bee Lab Docs".
func (l Location) rcloneSpec() string {
	if l.Kind == LocationLocal {
		return l.RootPath
	}
	return l.RemoteName + ":" + l.RootPath
}

// LocalFolderLocation wraps an arbitrary local folder path as an ephemeral,
// unsaved Location — e.g. a folder chosen via a native OS picker — so it can
// flow through the N-way scan/conflict-resolution machinery (ScanNWay,
// BuildNWayTransferPlan, the resolver in internal/ui) the exact same way any
// configured Location does. Never persisted; its ID only needs to be stable
// and unique for the lifetime of one scan/resolve/transfer session.
func LocalFolderLocation(name, absPath string) Location {
	return Location{ID: "local-folder:" + absPath, Name: name, Kind: LocationLocal, RootPath: absPath}
}

// SubLocation returns a copy of loc with relPath folded into its RootPath —
// for treating a specific subfolder of a Location as its own pseudo-Location
// root, e.g. so N-way helpers (which operate on a Location's own root, not
// root+relPath) can be reused against an arbitrary destination folder chosen
// via a folder browser rather than a Location's fixed experiments root.
func SubLocation(loc Location, relPath string) Location {
	if relPath == "" {
		return loc
	}
	sub := loc
	sub.RootPath = path.Join(loc.RootPath, relPath)
	sub.ID = loc.ID + "/" + relPath
	// Anchor benign-empty handling on the base location's root: if the folded
	// sub-path doesn't exist yet (a destination folder about to be created on
	// copy) but the base root is reachable, that's empty, not a listing error.
	sub.reachAnchor = loc.rcloneSpec()
	return sub
}

// joinSpec appends a relative sub-path (forward-slash separated, as rclone
// path specs always are regardless of host OS) onto an rclone spec string.
// path.Join is safe here because it treats the "remote:" prefix as an
// ordinary path segment — it never special-cases the colon.
func joinSpec(spec, relPath string) string {
	if relPath == "" {
		return spec
	}
	return path.Join(spec, relPath)
}
