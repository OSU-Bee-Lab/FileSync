package recorder

import (
	"context"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

// VolumeEventType distinguishes a volume appearing from one disappearing.
type VolumeEventType int

const (
	VolumeAttached VolumeEventType = iota
	VolumeDetached
)

// VolumeEvent is emitted by WatchVolumes each time a mounted volume
// appears or disappears.
type VolumeEvent struct {
	Type   VolumeEventType
	Volume Volume
}

// WatchVolumes polls the OS's mounted-filesystem list every interval and
// diffs it against the previous poll, closing the returned channel when
// ctx is done. This is the cross-platform (macOS/Linux/Windows)
// replacement for filesync's pyudev hotplug watcher (Linux-only) and the
// Olympus tool's psutil poll loop — both did the same diffing, just against
// different underlying APIs.
func WatchVolumes(ctx context.Context, interval time.Duration) <-chan VolumeEvent {
	events := make(chan VolumeEvent)

	go func() {
		defer close(events)

		known := make(map[string]Volume)

		poll := func() bool {
			parts, err := disk.PartitionsWithContext(ctx, false)
			if err != nil {
				return true
			}

			seen := make(map[string]Volume, len(parts))
			for _, p := range parts {
				if p.Mountpoint == "" {
					continue
				}
				seen[p.Mountpoint] = Volume{MountPoint: p.Mountpoint, FSType: p.Fstype}
			}

			for mp, v := range seen {
				if _, ok := known[mp]; !ok {
					select {
					case events <- VolumeEvent{Type: VolumeAttached, Volume: v}:
					case <-ctx.Done():
						return false
					}
				}
			}
			for mp, v := range known {
				if _, ok := seen[mp]; !ok {
					select {
					case events <- VolumeEvent{Type: VolumeDetached, Volume: v}:
					case <-ctx.Done():
						return false
					}
				}
			}
			known = seen
			return true
		}

		if !poll() {
			return
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !poll() {
					return
				}
			}
		}
	}()

	return events
}
