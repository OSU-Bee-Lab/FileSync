//go:build !darwin

package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
)

// chooseFolder falls back to Fyne's own folder dialog on platforms without
// a native picker wired up yet. cb is invoked once the user picks a folder
// or cancels (path == "").
func chooseFolder(win fyne.Window, cb func(path string, err error)) {
	dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil || uri == nil {
			cb("", err)
			return
		}
		cb(uri.Path(), nil)
	}, win)
}
