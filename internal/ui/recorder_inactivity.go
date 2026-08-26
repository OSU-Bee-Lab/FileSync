package ui

import (
	"context"
	"fmt"
	"sync"
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
// own End Sync button. Either way the prompt is unregistered from w first
// (see closePrompt), which is also what re-arms the countdown - so exactly
// one prompt is ever open, rather than a fresh one stacking on top of the
// unanswered one every timeout period.
func showInactivitySyncPrompt(s *state, w *recorderInactivityWatcher, onEnd func()) {
	var d dialog.Dialog
	endBtn := widget.NewButton("End Sync", func() {
		w.closePrompt()
		onEnd()
	})
	continueBtn := widget.NewButton("Continue Sync", func() {
		w.closePrompt()
	})
	continueBtn.Importance = widget.HighImportance
	d = dialog.NewCustomWithoutButtons("Sync paused due to inactivity",
		container.NewVBox(
			widget.NewLabel(fmt.Sprintf("No new recorders have been added in the last %v.",
				recorderInactivityTimeout(s))),
			actionRow(endBtn, continueBtn),
		), s.win)
	w.setPrompt(func() { d.Hide() })
	d.Show()
}

// recorderInactivityWatcher is the inactivity-timer state machine for
// Screen 2. It has no view into rows or the rest of recorderSyncScreen's
// state - only the idle flag (kept current by the caller) and the
// onTimeout callback - so it can be reasoned about, and tested,
// independent of the row-management code in screen_recorder_sync.go.
type recorderInactivityWatcher struct {
	resetInactivity chan struct{}

	// promptOpen is set from the moment run decides to fire the prompt
	// until it's answered or dismissed, and suppresses the countdown for
	// that whole time. Without it the poll below re-arms the timer the
	// instant the prompt is handed to the UI thread (idle is, by
	// definition, still true), so an unanswered prompt gets a second one
	// stacked on top of it every timeout period - a wall of dialogs over
	// the sync screen after a long break, hiding the recorders that were
	// in fact still being detected behind them. It's set before the prompt
	// reaches the UI thread rather than by the prompt itself, so a busy UI
	// thread can't let a poll slip in between.
	promptOpen atomic.Bool

	mu sync.Mutex
	// hidePrompt hides the open prompt's dialog; nil when none is open.
	hidePrompt func()
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

// setPrompt records how to hide the prompt now being shown, so activity
// elsewhere can dismiss it (see closePrompt). Called from the UI thread by
// showInactivitySyncPrompt.
func (w *recorderInactivityWatcher) setPrompt(hide func()) {
	w.mu.Lock()
	w.hidePrompt = hide
	w.mu.Unlock()
}

// closePrompt hides the inactivity prompt if one is open, then re-arms the
// countdown. It's what the prompt's own buttons call, and also what the
// attach/detach handlers call: a recorder being plugged in is the exact
// thing the prompt was waiting for, so leaving it up (over the row that
// recorder just started syncing on) makes the app look like it stopped
// detecting recorders. Safe to call from any goroutine and when no prompt
// is open; the hide hops onto the Fyne thread itself.
func (w *recorderInactivityWatcher) closePrompt() {
	w.mu.Lock()
	hide := w.hidePrompt
	w.hidePrompt = nil
	w.mu.Unlock()
	w.promptOpen.Store(false)
	if hide != nil {
		fyne.Do(hide)
	}
	w.signalActivity()
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
			// The screen is being left: an inactivity prompt still up
			// belongs to it, so take it down with it rather than leaving
			// it floating over whatever screen comes next.
			w.closePrompt()
			return
		case <-w.resetInactivity:
			// A recorder was attached or removed, or the user chose to
			// keep waiting: restart the countdown only if it's actually
			// applicable (nothing left actively syncing, and no prompt
			// still waiting on the user); otherwise make sure it stays off
			// until things go idle again.
			if idle.Load() && !w.promptOpen.Load() {
				restartTimer()
				running = true
			} else {
				stopTimer()
				running = false
			}
		case <-poll.C:
			i := idle.Load() && !w.promptOpen.Load()
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
			w.promptOpen.Store(true)
			fyne.Do(onTimeout)
		}
	}
}
