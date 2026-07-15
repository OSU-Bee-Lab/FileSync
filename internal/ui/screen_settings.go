package ui

import (
	"fmt"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
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

	backBtn := widget.NewButton("Back", func() { showHome(s) })

	content := container.NewBorder(
		container.NewVBox(widget.NewLabelWithStyle("Settings", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), widget.NewSeparator()),
		backBtn,
		nil, nil,
		container.NewVBox(
			debugCheck, debugHint,
			widget.NewSeparator(),
			widget.NewLabel("Recorder Sync inactivity timeout (minutes)"), inactivityEntry, inactivityHint,
			widget.NewSeparator(),
			widget.NewLabel("Checkers"), checkersEntry, checkersHint,
			widget.NewSeparator(),
			widget.NewLabel("Bandwidth limit (MiB/s)"), bwLimitEntry, bwLimitHint,
			widget.NewSeparator(),
			widget.NewLabel("Transfers"), transfersEntry, transfersHint,
		),
	)
	s.setContent(container.NewPadded(content))
}
