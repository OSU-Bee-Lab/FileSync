package ui

import (
	"context"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// scanJob is the closure that actually runs the real copy for one confirmed
// scan result. It's deliberately generic over Sync Experiments vs Pull Files
// - screen_scan.go and progress_run.go never need to know which flow
// produced a job.
type scanJob struct {
	Start func(ctx context.Context) (*syncengine.Job, <-chan syncengine.ProgressSnapshot)
	// Locs holds the Location(s) involved in Start's copy (source and, for
	// Sync Experiments, destination) so a failed job can offer to reconnect the right
	// remote instead of just printing rclone's raw error text.
	Locs []syncengine.Location
}

type scanTask struct {
	Label string
	Locs  []syncengine.Location
	// After holds indices, into the same []scanTask, of tasks whose copy
	// must finish successfully before this task's Start may run — this
	// task's source only holds the files once they have. It's what lets a
	// chained N-way fan-out (see syncengine.BuildNWayChainedTransferPlan)
	// download a file from a remote once and copy it on from the local
	// location it landed in, instead of downloading it once per local
	// destination. Indices may point forward as well as backward; the graph
	// must be acyclic. Honoured by runSync only: Scan never needs it, since
	// every scan just reads whatever is there at the time.
	After []int
	Scan  func(ctx context.Context, progress syncengine.ScanProgressFunc) (syncengine.ScanResult, error)
	Start func(ctx context.Context, result syncengine.ScanResult) (*syncengine.Job, <-chan syncengine.ProgressSnapshot)
}
