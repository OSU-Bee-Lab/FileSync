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
			role:   roleOfLocs(t.Locs),
			status: statusWaiting,
		}
		ps.scanResults[i] = syncengine.ScanResult{}
	}
	ps.selectedFoldIdx = 0
	ps.phase = phaseScanRunning
	ps.cancelling = false
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
				ps.expStates[i] = buildExpUIState(task.Label, roleOfLocs(task.Locs), result)
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
				ps.refreshUI()
				return
			}
			ps.phase = phaseScanComplete

			allDone := true
			for _, e := range ps.expStates {
				if e.status != statusDone {
					allDone = false
					break
				}
			}

			if allDone && ps.extras.onScanDone != nil {
				ps.extras.onScanDone()
				return
			}

			ps.refreshUI()

			if allDone && ps.extras.autoSync {
				// Pre-confirmed plan (see syncFlowExtras.autoSync): start
				// copying without a second Sync press.
				ps.runSync()
			}
		})
	}()
}

// setTaskSpeed records task i's latest transfer speed and redraws the
// speed label with the total across all running tasks. UI goroutine only.
func (ps *progressScreen) setTaskSpeed(i int, speed float64) {
	if i < 0 || i >= len(ps.taskSpeeds) {
		return
	}
	ps.taskSpeeds[i] = speed

	var total float64
	for _, s := range ps.taskSpeeds {
		total += s
	}
	if total > 0 {
		ps.speedLabel.SetText(fmt.Sprintf("Speed: %s/s", humanSpeed(total)))
	} else {
		ps.speedLabel.SetText("Speed: ---")
	}
}

// runSync rebuilds expStates from the confirmed scan results and runs the
// real copy for every task concurrently (bounded by maxConcurrentTasks).
func (ps *progressScreen) runSync() {
	// Rebuild expStates from scanResults so progress is reset when
	// re-running after a cancellation.
	for i, t := range ps.tasks {
		ps.expStates[i] = buildExpUIState(t.Label, roleOfLocs(t.Locs), ps.scanResults[i])
	}
	ps.selectedFoldIdx = 0
	ps.phase = phaseSyncing
	ps.cancelling = false
	ps.taskSpeeds = make([]float64, len(ps.tasks))
	ps.speedLabel.SetText("Speed: ---")
	ps.speedLabel.Show()
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

	// done[i] is closed when task i has finished (however it ended), with
	// succeeded[i] written first — a task waiting on i reads it only after
	// that close, so the channel carries the happens-before. This is what
	// enforces scanTask.After: a chained copy can't start until the copy
	// feeding its source has landed. Every task closes its channel on every
	// path, including the early bail-outs, so a waiter can never hang.
	done := make([]chan struct{}, len(ps.tasks))
	succeeded := make([]bool, len(ps.tasks))
	for i := range done {
		done[i] = make(chan struct{})
	}

	go func() {
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxConcurrentTasks)

		runOne := func(i int, j scanJob) {
			ok := false
			defer func() {
				succeeded[i] = ok
				close(done[i])
				wg.Done()
			}()

			// Wait for dependencies before taking a semaphore slot, never
			// while holding one — a slot held for the whole wait would let
			// maxConcurrentTasks waiting tasks starve out the very tasks
			// they're waiting for.
			blockedBy := -1
			for _, dep := range ps.tasks[i].After {
				<-done[dep]
				if !succeeded[dep] {
					blockedBy = dep
					break
				}
			}
			if blockedBy >= 0 {
				depLabel := ps.tasks[blockedBy].Label
				srcName := "its source"
				if len(j.Locs) > 0 {
					srcName = j.Locs[0].Name
				}
				cancelled := ctx.Err() != nil
				fyne.Do(func() {
					if cancelled {
						ps.expStates[i].status = statusCanceled
					} else {
						ps.expStates[i].status = statusError
						ps.expStates[i].hasError = true
						ps.expStates[i].err = fmt.Errorf("skipped: %s did not finish, so %s does not have these files to copy on",
							depLabel, srcName)
					}
					ps.refreshUI()
				})
				return
			}

			sem <- struct{}{}
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
						ps.retryLabel.Text = retryNoticeText(snap.RetryAttempt, snap.RetryMax)
						ps.retryLabel.Refresh()
						ps.retryLabel.Show()
					} else {
						ps.retryLabel.Hide()
					}

					ps.setTaskSpeed(i, snap.Speed)

					// Force refreshing the active folders/files list during sync
					if ps.selectedExpIdx == i {
						ps.refreshFolders()
						ps.refreshFiles()
					}
					ps.refreshUI()
				})
			}

			// Recorded outside the fyne.Do below because that runs later, on
			// the UI goroutine, while anything waiting on this task (see
			// scanTask.After) reads succeeded[i] the moment done[i] closes.
			ok = final.Status != syncengine.JobError && final.Status != syncengine.JobCanceled

			fyne.Do(func() {
				// This task is no longer moving bytes, so it must stop
				// contributing to the summed speed - otherwise a finished
				// job's last reading would inflate the total for as long as
				// the slower ones keep running.
				ps.setTaskSpeed(i, 0)

				// The wind-down this task was reporting is over (however it
				// ended), so it must stop claiming to be finishing up -
				// otherwise the last snapshot's flag would keep the caption on
				// screen for as long as the other tasks keep running.
				ps.expStates[i].finalizing = false

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

		// Every task gets a goroutine up front — the semaphore, taken inside
		// runOne, is what actually bounds concurrency. Launching them all
		// keeps a task that's only waiting on a dependency from occupying a
		// slot it isn't using.
		for i, j := range jobs {
			wg.Add(1)
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
