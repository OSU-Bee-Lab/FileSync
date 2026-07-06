//go:build !darwin

package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
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

// chooseFileSave falls back to Fyne's own file-save dialog on platforms
// without a native picker wired up. path == "" means the user cancelled.
func chooseFileSave(win fyne.Window, defaultName string, cb func(path string, err error)) {
	d := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
		if err != nil || uc == nil {
			cb("", err)
			return
		}
		path := uc.URI().Path()
		uc.Close()
		cb(path, nil)
	}, win)
	d.SetFileName(defaultName)
	d.Show()
}

// chooseFileOpen falls back to Fyne's own file-open dialog on platforms
// without a native picker wired up. path == "" means the user cancelled.
func chooseFileOpen(win fyne.Window, exts []string, cb func(path string, err error)) {
	d := dialog.NewFileOpen(func(uc fyne.URIReadCloser, err error) {
		if err != nil || uc == nil {
			cb("", err)
			return
		}
		path := uc.URI().Path()
		uc.Close()
		cb(path, nil)
	}, win)
	if len(exts) > 0 {
		dotted := make([]string, len(exts))
		for i, e := range exts {
			dotted[i] = "." + e
		}
		d.SetFilter(storage.NewExtensionFileFilter(dotted))
	}
	d.Show()
}
