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

func locationNames(locs []syncengine.Location) []string {
	out := make([]string, len(locs))
	for i, l := range locs {
		out[i] = l.Name
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

// joinRel joins a browsing breadcrumb path with a child name, both always
// forward-slash separated (an rclone-relative path, not an OS path).
func joinRel(base, name string) string {
	if base == "" {
		return name
	}
	return base + "/" + name
}
