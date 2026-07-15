//go:build linux

package ui

import (
	"os/exec"
	"strings"

	"fyne.io/fyne/v2"
)

// chooseFolder shells out to zenity's native GTK folder picker when zenity
// is available, falling back to Fyne's in-app dialog on distros/systems
// that don't have it installed (e.g. minimal or non-GNOME setups). cb is
// invoked on the Fyne UI thread once the user picks a folder or cancels
// (path == "").
func chooseFolder(win fyne.Window, cb func(path string, err error)) {
	if _, err := exec.LookPath("zenity"); err != nil {
		fyneChooseFolder(win, cb)
		return
	}
	go func() {
		out, err := exec.Command("zenity", "--file-selection", "--directory", "--title=Choose folder").Output()
		path, err := zenityResult(out, err)
		fyne.Do(func() { cb(path, err) })
	}()
}

// chooseFileSave shells out to zenity's native GTK save dialog when
// available. defaultName seeds the filename field. cb is invoked on the
// Fyne UI thread; path == "" means the user cancelled.
func chooseFileSave(win fyne.Window, defaultName string, cb func(path string, err error)) {
	if _, err := exec.LookPath("zenity"); err != nil {
		fyneChooseFileSave(win, defaultName, cb)
		return
	}
	go func() {
		out, err := exec.Command("zenity", "--file-selection", "--save", "--confirm-overwrite",
			"--title=Save as", "--filename="+defaultName).Output()
		path, err := zenityResult(out, err)
		fyne.Do(func() { cb(path, err) })
	}()
}

// chooseFileOpen shells out to zenity's native GTK open dialog when
// available. exts limits selectable extensions (e.g. "json"); empty allows
// any. cb runs on the Fyne UI thread; "" means cancel.
func chooseFileOpen(win fyne.Window, exts []string, cb func(path string, err error)) {
	if _, err := exec.LookPath("zenity"); err != nil {
		fyneChooseFileOpen(win, exts, cb)
		return
	}
	go func() {
		args := []string{"--file-selection", "--title=Choose file"}
		if len(exts) > 0 {
			patterns := make([]string, len(exts))
			for i, e := range exts {
				patterns[i] = "*." + e
			}
			args = append(args, "--file-filter="+strings.Join(patterns, " "))
		}
		out, err := exec.Command("zenity", args...).Output()
		path, err := zenityResult(out, err)
		fyne.Do(func() { cb(path, err) })
	}()
}

// zenityResult interprets zenity's output/exit status: a zero exit with
// output means a path was chosen; exit status 1 means the user cancelled
// (zenity's documented Cancel code, regardless of anything written to
// stderr — some distros' zenity/GTK builds emit warnings there even on a
// clean cancel); anything else is a real error.
func zenityResult(out []byte, err error) (string, error) {
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
