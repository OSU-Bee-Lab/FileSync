package ui

import (
	"fmt"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func plural(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
}

// locationNames returns the names of every enabled Location, for
// populating a from/to picker. Disabled locations are left out so a
// temporarily-suspended location (see Location.Enabled) can't be picked
// as a live sync endpoint.
func locationNames(locs []syncengine.Location) []string {
	var out []string
	for _, l := range locs {
		if l.Enabled {
			out = append(out, l.Name)
		}
	}
	return out
}

func findLocation(locs []syncengine.Location, name string) *syncengine.Location {
	for i := range locs {
		if locs[i].Name == name {
			return &locs[i]
		}
	}
	return nil
}

func findLocationByID(locs []syncengine.Location, id string) *syncengine.Location {
	for i := range locs {
		if locs[i].ID == id {
			return &locs[i]
		}
	}
	return nil
}

// locationNamesByKind returns the names of every enabled Location of the
// given kind, e.g. for populating a destination picker that only makes
// sense for local folders or only for cloud remotes.
func locationNamesByKind(locs []syncengine.Location, kind syncengine.LocationKind) []string {
	var out []string
	for _, l := range locs {
		if l.Kind == kind && l.Enabled {
			out = append(out, l.Name)
		}
	}
	return out
}

// joinRel joins a browsing breadcrumb path with a child name, both always
// forward-slash separated (an rclone-relative path, not an OS path).
func joinRel(base, name string) string {
	if base == "" {
		return name
	}
	return base + "/" + name
}
