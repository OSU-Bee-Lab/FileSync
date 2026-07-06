package ui

import (
	"context"

	"github.com/OSU-Bee-Lab/expsync/internal/syncengine"
)

// previewJob is one dry-run result the user is being asked to confirm,
// paired with the closure that actually runs the real copy for it. It's
// deliberately generic over Backup vs Download - screen_preview.go and
// screen_progress.go never need to know which flow produced a job.
type previewJob struct {
	Label  string
	Result syncengine.PreviewResult
	Start  func(ctx context.Context) (*syncengine.Job, <-chan syncengine.ProgressSnapshot)
	// Locs holds the Location(s) involved in Start's copy (source and, for
	// Backup, destination) so a failed job can offer to reconnect the right
	// remote instead of just printing rclone's raw error text.
	Locs []syncengine.Location
}

type previewTask struct {
	Label   string
	Locs    []syncengine.Location
	Preview func(ctx context.Context, progress syncengine.PreviewProgressFunc) (syncengine.PreviewResult, error)
	Start   func(ctx context.Context, result syncengine.PreviewResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot)
}
