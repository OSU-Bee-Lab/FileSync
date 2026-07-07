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
// ExpSync's config.json rather than a bare int, so the file stays
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

// Location is one storage root a collaborator has configured — a local
// drive/folder or a remote (SharePoint, Drive, Dropbox, S3, ...). Nothing
// about a Location is hardcoded: every field is user-supplied and
// persisted per-machine by internal/appconfig.
type Location struct {
	ID   string       `json:"id"`
	Name string       `json:"name"`
	Kind LocationKind `json:"kind"`

	// RemoteName is the rclone remote name (as stored in rclone's own
	// config file) backing this location. Empty when Kind == LocationLocal.
	RemoteName string `json:"remoteName,omitempty"`

	// RootPath is either an absolute local filesystem path (LocationLocal)
	// or a path within the remote (LocationRemote), e.g. "" or
	// "Bee Lab Docs". It points directly at the experiments/ root — the
	// folder whose immediate children are experiment directories.
	RootPath string `json:"rootPath"`

	// Enabled controls whether this Location is offered anywhere a
	// Location is picked (Sync Experiments, Pull Files, recorder destination/upload).
	// Disabling it (rather than removing it) lets a temporarily
	// unavailable location - e.g. an unplugged external drive - be
	// suspended without losing its settings.
	Enabled bool `json:"enabled"`
}

// rcloneSpec returns the fs.NewFs-ready path string for this location, e.g.
// "/Volumes/BeeLabServer" or "sharepoint-osu:Bee Lab Docs".
func (l Location) rcloneSpec() string {
	if l.Kind == LocationLocal {
		return l.RootPath
	}
	return l.RemoteName + ":" + l.RootPath
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
