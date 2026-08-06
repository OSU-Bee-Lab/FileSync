package ui

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/OSU-Bee-Lab/filesync/internal/syncengine"
)

// showSettings is FileSync's app-level preferences screen (as opposed to
// per-Location or per-experiment settings, which live on their own screens).
func showSettings(s *state) {
	debugCheck := widget.NewCheck("Debug mode (verbose console logging of scan/copy progress and rclone)", nil)
	debugCheck.SetChecked(s.cfg.DebugMode)
	debugCheck.OnChanged = func(checked bool) {
		s.cfg.DebugMode = checked
		syncengine.SetDebugLogging(checked)
		s.saveConfig()
	}

	debugHint := widget.NewLabel("When enabled, FileSync prints what it's scanning/copying to the console " +
		"(stdout/stderr) - useful for diagnosing a sync or scan that seems stuck.")
	debugHint.Wrapping = fyne.TextWrapWord

	inactivityEntry := widget.NewEntry()
	inactivityEntry.SetText(strconv.Itoa(s.cfg.RecorderInactivityTimeoutMinutes))
	inactivityEntry.OnChanged = func(text string) {
		n, err := strconv.Atoi(text)
		if err != nil || n <= 0 {
			return
		}
		s.cfg.RecorderInactivityTimeoutMinutes = n
		s.saveConfig()
	}
	inactivityHint := widget.NewLabel("How long Recorder Sync waits, with nothing actively syncing, before " +
		"pausing and asking whether to keep waiting or end the session.")
	inactivityHint.Wrapping = fyne.TextWrapWord

	checkersEntry := widget.NewEntry()
	checkersEntry.SetText(strconv.Itoa(s.cfg.Checkers))
	checkersEntry.OnChanged = func(text string) {
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 {
			checkersEntry.SetText(strconv.Itoa(s.cfg.Checkers))
			return
		}
		s.cfg.Checkers = n
		syncengine.SetCheckers(n)
		s.saveConfig()
	}
	checkersHint := widget.NewLabel("Number of files checked concurrently during a scan/copy (rclone's " +
		fmt.Sprintf("--checkers). Default is %d. ", syncengine.DefaultCheckers) +
		"Raising this can speed up scans over a fast, low-latency connection; lowering it can help on slow " +
		"or rate-limited remotes.")
	checkersHint.Wrapping = fyne.TextWrapWord

	bwLimitEntry := widget.NewEntry()
	bwLimitEntry.SetText(strconv.Itoa(s.cfg.BwLimitMiBPerSec))
	bwLimitEntry.OnChanged = func(text string) {
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 {
			return
		}
		s.cfg.BwLimitMiBPerSec = n
		syncengine.SetBwLimitMiBPerSec(n)
		s.saveConfig()
	}
	bwLimitHint := widget.NewLabel("Caps transfer speed in MiB/s across all syncs, so FileSync doesn't " +
		"saturate the network. 0 means unlimited.")
	bwLimitHint.Wrapping = fyne.TextWrapWord

	transfersEntry := widget.NewEntry()
	transfersEntry.SetText(strconv.Itoa(s.cfg.Transfers))
	transfersEntry.OnChanged = func(text string) {
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 {
			transfersEntry.SetText(strconv.Itoa(s.cfg.Transfers))
			return
		}
		s.cfg.Transfers = n
		syncengine.SetTransfers(n)
		s.saveConfig()
	}
	transfersHint := widget.NewLabel("Number of files copied concurrently within a single scan/copy job " +
		fmt.Sprintf("(rclone's --transfers). Default is %d. ", syncengine.DefaultTransfers) +
		"Raising this can speed up an N-way sync with many small files over a fast connection; lowering it " +
		"can help on slow or rate-limited remotes.")
	transfersHint.Wrapping = fyne.TextWrapWord

	http2Check := widget.NewCheck("Use HTTP/2 for remote transfers", func(enabled bool) {
		s.cfg.HTTP2Enabled = enabled
		syncengine.SetHTTP2Enabled(enabled)
		s.saveConfig()
	})
	http2Check.SetChecked(s.cfg.HTTP2Enabled)
	http2Hint := widget.NewLabel("Off by default. HTTP/2 sends every transfer down one connection, so when that " +
		"connection drops — which SharePoint and OneDrive do routinely — every file in flight fails at once and " +
		"restarts from the beginning. On HTTP/1.1 each transfer has its own connection, so a drop costs one file. " +
		"There's no meaningful speed difference for audio-sized files. Turn this on only if a remote specifically " +
		"needs it. Changing this applies to remotes you haven't synced in the last few minutes; restart FileSync " +
		"to be sure it applies everywhere.")
	http2Hint.Wrapping = fyne.TextWrapWord

	// Retries: a checkbox for the common case ("just keep going") with the
	// entry only in play once the user opts into a finite budget, rather
	// than a bare number field where 0 silently means "unlimited".
	retriesEntry := widget.NewEntry()
	retriesEntry.SetText(strconv.Itoa(max(s.cfg.CopyRetries, 2)))
	retriesEntry.OnChanged = func(text string) {
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 {
			return
		}
		s.cfg.CopyRetries = n
		syncengine.SetCopyRetries(n)
		s.saveConfig()
	}
	retriesForever := widget.NewCheck("Keep retrying until the sync gets through", nil)
	retriesForever.OnChanged = func(forever bool) {
		if forever {
			retriesEntry.Disable()
			s.cfg.CopyRetries = syncengine.RetriesUnlimited
			syncengine.SetCopyRetries(syncengine.RetriesUnlimited)
			s.saveConfig()
			return
		}
		retriesEntry.Enable()
		retriesEntry.OnChanged(retriesEntry.Text)
	}
	retriesForever.SetChecked(s.cfg.CopyRetries == syncengine.RetriesUnlimited)
	if retriesForever.Checked {
		retriesEntry.Disable()
	}
	retriesHint := widget.NewLabel("How many times a sync retries after a dropped connection or a network " +
		"outage before giving up. Retries wait longer each time (up to 5 minutes), and resume immediately once " +
		"the connection is back, so an overnight upload survives the internet going down for a while. Errors " +
		"that retrying can't fix — a sign-in that expired, a full drive, a file the remote refuses — still stop " +
		"straight away, and Cancel always works.")
	retriesHint.Wrapping = fyne.TextWrapWord

	excludeRows := container.NewVBox()
	rebuildExcludeRows(s, excludeRows)

	excludeHint := widget.NewLabel("Files matching an enabled pattern below are never scanned or copied. " +
		"Patterns are gitignore-style: a name with no \"/\" (e.g. \".DS_Store\") matches at any depth; " +
		"\"*\" and \"?\" wildcards are supported (e.g. \"._*\"). Uncheck a row to keep it without applying it; " +
		"the trash icon removes it.")
	excludeHint.Wrapping = fyne.TextWrapWord

	addPatternEntry := widget.NewEntry()
	addPatternEntry.SetPlaceHolder("e.g. *.tmp")
	addPattern := func() {
		pattern := strings.TrimSpace(addPatternEntry.Text)
		if pattern == "" {
			return
		}
		s.cfg.DefaultFilter.ExcludeRules = append(s.cfg.DefaultFilter.ExcludeRules, syncengine.ExcludeRule{Pattern: pattern, Enabled: true})
		s.saveConfig()
		addPatternEntry.SetText("")
		rebuildExcludeRows(s, excludeRows)
	}
	addPatternEntry.OnSubmitted = func(string) { addPattern() }
	addPatternBtn := widget.NewButtonWithIcon("Add", theme.ContentAddIcon(), addPattern)

	backBtn := widget.NewButton("Back", func() { showHome(s) })

	scroll := container.NewVScroll(container.NewVBox(
		debugCheck, debugHint,
		widget.NewSeparator(),
		widget.NewLabel("Recorder Sync inactivity timeout (minutes)"), inactivityEntry, inactivityHint,
		widget.NewSeparator(),
		widget.NewLabel("Checkers"), checkersEntry, checkersHint,
		widget.NewSeparator(),
		widget.NewLabel("Bandwidth limit (MiB/s)"), bwLimitEntry, bwLimitHint,
		widget.NewSeparator(),
		widget.NewLabel("Transfers"), transfersEntry, transfersHint,
		widget.NewSeparator(),
		widget.NewLabel("Retries"), retriesForever, retriesEntry, retriesHint,
		widget.NewSeparator(),
		widget.NewLabel("Connections"), http2Check, http2Hint,
		widget.NewSeparator(),
		widget.NewLabel("Excluded files/folders"), excludeHint, excludeRows,
		container.NewBorder(nil, nil, nil, addPatternBtn, addPatternEntry),
	))
	fixEntryScrolling(scroll.Content, scroll)

	content := container.NewBorder(
		container.NewVBox(widget.NewLabelWithStyle("Settings", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), widget.NewSeparator()),
		backBtn,
		nil, nil,
		scroll,
	)
	s.setContent(centerMaxWidth(container.NewPadded(content), windowSize.Width))
}

// rebuildExcludeRows repopulates rows with one line per
// s.cfg.DefaultFilter.ExcludeRules entry - a checkbox (enabled state) and a
// delete button - and is called again after every toggle/add/delete so the
// list always reflects the current config.
func rebuildExcludeRows(s *state, rows *fyne.Container) {
	rows.RemoveAll()
	for i, rule := range s.cfg.DefaultFilter.ExcludeRules {
		i := i
		check := widget.NewCheck(rule.Pattern, func(enabled bool) {
			s.cfg.DefaultFilter.ExcludeRules[i].Enabled = enabled
			s.saveConfig()
		})
		check.SetChecked(rule.Enabled)
		deleteBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
			rules := s.cfg.DefaultFilter.ExcludeRules
			s.cfg.DefaultFilter.ExcludeRules = append(append([]syncengine.ExcludeRule{}, rules[:i]...), rules[i+1:]...)
			s.saveConfig()
			rebuildExcludeRows(s, rows)
		})
		rows.Add(container.NewBorder(nil, nil, nil, deleteBtn, check))
	}
	rows.Refresh()
}
