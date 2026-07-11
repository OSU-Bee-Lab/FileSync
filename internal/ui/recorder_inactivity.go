package ui

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// defaultRecorderInactivityTimeoutMinutes is used if Config.
// RecorderInactivityTimeoutMinutes is somehow unset (see appconfig.Default).
const defaultRecorderInactivityTimeoutMinutes = 5

// recorderInactivityTimeout returns how long showRecorderSync waits for a
// new recorder to attach before pausing the session and prompting the
// user, per the Settings screen's configurable value.
func recorderInactivityTimeout(s *state) time.Duration {
	minutes := s.cfg.RecorderInactivityTimeoutMinutes
	if minutes <= 0 {
		minutes = defaultRecorderInactivityTimeoutMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// showInactivitySyncPrompt is shown when no new recorder has attached within
// recorderInactivityTimeout during an active sync session. "Continue Sync"
// dismisses the prompt and resets the timer; "End Sync" mirrors the screen's
// own End Sync button.
func showInactivitySyncPrompt(s *state, onContinue func(), onEnd func()) {
	var d dialog.Dialog
	endBtn := widget.NewButton("End Sync", func() {
		d.Hide()
		onEnd()
	})
	continueBtn := widget.NewButton("Continue Sync", func() {
		d.Hide()
		onContinue()
	})
	continueBtn.Importance = widget.HighImportance
	d = dialog.NewCustomWithoutButtons("Sync paused due to inactivity",
		container.NewVBox(
			widget.NewLabel(fmt.Sprintf("No new recorders have been added in the last %v.",
				recorderInactivityTimeout(s))),
			container.NewHBox(endBtn, continueBtn),
		), s.win)
	d.Show()
}

// recorderInactivityWatcher is the inactivity-timer state machine for
// Screen 2. It has no view into rows or the rest of recorderSyncScreen's
// state - only the idle flag (kept current by the caller) and the
// onTimeout callback - so it can be reasoned about, and tested,
// independent of the row-management code in screen_recorder_sync.go.
type recorderInactivityWatcher struct {
	resetInactivity chan struct{}
}

func newRecorderInactivityWatcher() *recorderInactivityWatcher {
	return &recorderInactivityWatcher{resetInactivity: make(chan struct{}, 1)}
}

// signalActivity notifies the watcher that a recorder was attached or
// removed, or that the user chose to keep waiting from the inactivity
// prompt, so the countdown (re)starts if applicable (i.e. idle is true).
func (w *recorderInactivityWatcher) signalActivity() {
	select {
	case w.resetInactivity <- struct{}{}:
	default:
	}
}

// run is the inactivity-timer goroutine: it polls idle far more often than
// the timeout itself fires, so the countdown starts promptly once the last
// active recorder finishes (or is removed) rather than only on the next
// explicit signalActivity call. It blocks until watchCtx is canceled, and
// is meant to be launched with `go`. onTimeout fires on the Fyne UI thread.
func (w *recorderInactivityWatcher) run(watchCtx context.Context, s *state, idle *atomic.Bool, onTimeout func()) {
	const pollInterval = 2 * time.Second
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()

	var timer *time.Timer
	var timerC <-chan time.Time
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
			timerC = nil
		}
	}
	restartTimer := func() {
		stopTimer()
		timer = time.NewTimer(recorderInactivityTimeout(s))
		timerC = timer.C
	}

	running := false
	for {
		select {
		case <-watchCtx.Done():
			stopTimer()
			return
		case <-w.resetInactivity:
			// A recorder was attached or removed, or the user chose to
			// keep waiting: restart the countdown only if it's actually
			// applicable (nothing left actively syncing); otherwise make
			// sure it stays off until things go idle again.
			if idle.Load() {
				restartTimer()
				running = true
			} else {
				stopTimer()
				running = false
			}
		case <-poll.C:
			i := idle.Load()
			if i && !running {
				restartTimer()
				running = true
			} else if !i && running {
				stopTimer()
				running = false
			}
		case <-timerC:
			stopTimer()
			running = false
			fyne.Do(onTimeout)
		}
	}
}
