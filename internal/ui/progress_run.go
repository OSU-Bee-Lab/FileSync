package ui

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// This file holds the scan and sync goroutine drivers for progressScreen
// (see progress_screen.go for the struct and rendering, progress_model.go
// for the pure per-experiment mutations these drivers call into).

// maxConcurrentTasks bounds how many (experiment, destination) scan/sync
// tasks run at once. Tasks are independent (each opens its own src/dst Fs),
// so running them concurrently means a slow cloud destination no longer
// blocks a fast local one. Capped rather than unbounded to avoid hammering
// a single remote's API with too many simultaneous listings/transfers.
const maxConcurrentTasks = 4

// runScan resets state and (re-)runs the scan goroutine. It is called once
// at startup and again if the user clicks Scan after a cancellation.
func (ps *progressScreen) runScan() {
	// Reset experiment states so the lists are clean on re-run.
	for i, t := range ps.tasks {
		ps.expStates[i] = &expUIState{
			label:  t.Label,
			status: statusWaiting,
		}
		ps.scanResults[i] = syncengine.ScanResult{}
	}
	ps.selectedFoldIdx = 0
	ps.phase = phaseScanRunning
	ps.refreshUI()
	if len(ps.tasks) > 0 {
		ps.expList.Select(0)
	}

	// Create this run's context on the UI goroutine, before launching the
	// worker, so the Cancel button (also on the UI goroutine) always
	// observes the current run's cancel — never a stale one or nil in the
	// window before the goroutine is scheduled, and never via a data race.
	ctx, cancel := context.WithCancel(context.Background())
	ps.activeCancel = cancel

	go func() {
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxConcurrentTasks)

		var mu sync.Mutex
		cancelled := false

		runOne := func(i int, task scanTask) {
			defer wg.Done()
			defer func() { <-sem }()

			if ctx.Err() != nil {
				mu.Lock()
				cancelled = true
				mu.Unlock()
				return
			}

			fyne.Do(func() {
				ps.expStates[i].status = statusRunning
				ps.refreshUI()
			})

			result, err := task.Scan(ctx, func(p syncengine.ScanProgress) {
				fyne.Do(func() {
					ps.expStates[i].applyScanProgress(p)
					ps.refreshUI()
				})
			})

			if err != nil {
				isCanceled := errors.Is(err, context.Canceled)
				if isCanceled {
					mu.Lock()
					cancelled = true
					mu.Unlock()
				}
				fyne.Do(func() {
					if isCanceled {
						ps.expStates[i].status = statusCanceled
					} else {
						ps.expStates[i].status = statusError
						ps.expStates[i].err = err
						ps.expStates[i].hasError = true
					}
					ps.refreshUI()
				})
				if !isCanceled && isAuthError(err) {
					fyne.Do(func() {
						showLocationError(ps.s, err, task.Locs...)
					})
				}
				return
			}

			ps.scanResults[i] = result
			fyne.Do(func() {
				ps.expStates[i] = buildExpUIState(task.Label, result)
				ps.expStates[i].status = statusDone
				if ps.selectedExpIdx == i {
					ps.selectedFoldIdx = 0
					ps.refreshFolders()
					ps.refreshFiles()
					ps.expList.Select(widget.ListItemID(i))
				}
				ps.refreshUI()
			})
		}

		for i, task := range ps.tasks {
			wg.Add(1)
			sem <- struct{}{}
			go runOne(i, task)
		}
		wg.Wait()
		cancel()

		fyne.Do(func() {
			mu.Lock()
			wasCancelled := cancelled
			mu.Unlock()
			if wasCancelled {
				ps.phase = phaseScanCancelled
			} else {
				ps.phase = phaseScanComplete
			}
			ps.refreshUI()

			if ps.extras.autoSync && ps.phase == phaseScanComplete {
				// Pre-confirmed plan (see syncFlowExtras.autoSync): start
				// copying without a second Sync press — but only if every
				// task's instant "scan" replay actually succeeded.
				for _, e := range ps.expStates {
					if e.status != statusDone {
						return
					}
				}
				ps.runSync()
			}
		})
	}()
}

// runSync rebuilds expStates from the confirmed scan results and runs the
// real copy for every task concurrently (bounded by maxConcurrentTasks).
func (ps *progressScreen) runSync() {
	// Rebuild expStates from scanResults so progress is reset when
	// re-running after a cancellation.
	for i, t := range ps.tasks {
		ps.expStates[i] = buildExpUIState(t.Label, ps.scanResults[i])
	}
	ps.selectedFoldIdx = 0
	ps.phase = phaseSyncing
	ps.refreshUI()

	jobs := make([]scanJob, len(ps.tasks))
	for i, t := range ps.tasks {
		jobs[i] = scanJob{
			Locs: t.Locs,
			Start: func(ctx context.Context) (*syncengine.Job, <-chan syncengine.ProgressSnapshot) {
				return t.Start(ctx, ps.scanResults[i])
			},
		}
	}

	// Create this run's context on the UI goroutine, before launching the
	// worker, so the Cancel button (also on the UI goroutine) always
	// observes the current run's cancel — never a stale one or nil in the
	// window before the goroutine is scheduled, and never via a data race.
	ctx, cancel := context.WithCancel(context.Background())
	ps.activeCancel = cancel

	go func() {
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxConcurrentTasks)

		runOne := func(i int, j scanJob) {
			defer wg.Done()
			defer func() { <-sem }()

			if ctx.Err() != nil {
				return
			}

			fyne.Do(func() {
				ps.expStates[i].status = statusRunning
				ps.refreshUI()
			})

			job, progress := j.Start(ctx)
			_ = job

			var final syncengine.ProgressSnapshot
			for snap := range progress {
				final = snap
				fyne.Do(func() {
					ps.expStates[i].applySyncSnapshot(snap)

					if snap.Retrying {
						ps.retryLabel.Text = fmt.Sprintf("⚠ Connection hiccup, retrying (%d/%d)…", snap.RetryAttempt, snap.RetryMax)
						ps.retryLabel.Refresh()
						ps.retryLabel.Show()
					} else {
						ps.retryLabel.Hide()
					}

					if snap.Speed > 0 {
						ps.speedLabel.SetText(fmt.Sprintf("Speed: %s/s", humanSpeed(snap.Speed)))
						ps.speedLabel.Show()
					} else {
						ps.speedLabel.Hide()
					}

					// Force refreshing the active folders/files list during sync
					if ps.selectedExpIdx == i {
						ps.refreshFolders()
						ps.refreshFiles()
					}
					ps.refreshUI()
				})
			}

			fyne.Do(func() {
				statusText := statusDone
				var jobErr error
				switch final.Status {
				case syncengine.JobError:
					statusText = statusError
					jobErr = final.Err
					ps.expStates[i].hasError = true
					ps.expStates[i].err = final.Err
					if isAuthError(final.Err) {
						showLocationError(ps.s, final.Err, j.Locs...)
					}
				case syncengine.JobCanceled:
					statusText = statusCanceled
				}
				ps.expStates[i].status = statusText
				ps.expStates[i].err = jobErr

				if statusText == statusDone {
					ps.expStates[i].markDone()
				}

				if ps.selectedExpIdx == i {
					ps.refreshFolders()
					ps.refreshFiles()
				}
				ps.refreshUI()
			})
		}

		for i, j := range jobs {
			wg.Add(1)
			sem <- struct{}{}
			go runOne(i, j)
		}
		wg.Wait()

		// Check cancellation before calling cancel() so ctx.Err() reflects
		// whether the user cancelled, not the cleanup cancel below.
		wasCancelled := ctx.Err() != nil
		cancel()

		fyne.Do(func() {
			if wasCancelled {
				ps.phase = phaseSyncCancelled
			} else {
				ps.phase = phaseSyncComplete
			}
			ps.speedLabel.Hide()
			ps.retryLabel.Hide()
			ps.refreshUI()
		})
	}()
}
