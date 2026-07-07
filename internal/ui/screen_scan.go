package ui

import (
	"context"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

// scanJob is one scan result the user is being asked to confirm,
// paired with the closure that actually runs the real copy for it. It's
// deliberately generic over Sync Experiments vs Pull Files - screen_scan.go and
// screen_progress.go never need to know which flow produced a job.
type scanJob struct {
	Label  string
	Result syncengine.ScanResult
	Start  func(ctx context.Context) (*syncengine.Job, <-chan syncengine.ProgressSnapshot)
	// Locs holds the Location(s) involved in Start's copy (source and, for
	// Sync Experiments, destination) so a failed job can offer to reconnect the right
	// remote instead of just printing rclone's raw error text.
	Locs []syncengine.Location
}

type scanTask struct {
	Label string
	Locs  []syncengine.Location
	Scan  func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error)
	Start func(ctx context.Context, result syncengine.ScanResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot)
}
