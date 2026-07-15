//go:build !darwin && !linux && !windows

package ui

import "fyne.io/fyne/v2"

// chooseFolder falls back to Fyne's own folder dialog on platforms without
// a native picker wired up yet. cb is invoked once the user picks a folder
// or cancels (path == "").
func chooseFolder(win fyne.Window, cb func(path string, err error)) {
	fyneChooseFolder(win, cb)
}

// chooseFileSave falls back to Fyne's own file-save dialog on platforms
// without a native picker wired up. path == "" means the user cancelled.
func chooseFileSave(win fyne.Window, defaultName string, cb func(path string, err error)) {
	fyneChooseFileSave(win, defaultName, cb)
}

// chooseFileOpen falls back to Fyne's own file-open dialog on platforms
// without a native picker wired up. path == "" means the user cancelled.
func chooseFileOpen(win fyne.Window, exts []string, cb func(path string, err error)) {
	fyneChooseFileOpen(win, exts, cb)
}
