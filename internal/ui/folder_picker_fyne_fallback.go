//go:build !darwin

package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
)

// fyneChooseFolder shows Fyne's own in-app folder dialog. Used as a fallback
// on platforms (or systems) without a native picker available. cb is invoked
// once the user picks a folder or cancels (path == "").
func fyneChooseFolder(win fyne.Window, cb func(path string, err error)) {
	dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil || uri == nil {
			cb("", err)
			return
		}
		cb(uri.Path(), nil)
	}, win)
}

// fyneChooseFileSave shows Fyne's own in-app file-save dialog. path == ""
// means the user cancelled.
func fyneChooseFileSave(win fyne.Window, defaultName string, cb func(path string, err error)) {
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

// fyneChooseFileOpen shows Fyne's own in-app file-open dialog. path == ""
// means the user cancelled.
func fyneChooseFileOpen(win fyne.Window, exts []string, cb func(path string, err error)) {
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
