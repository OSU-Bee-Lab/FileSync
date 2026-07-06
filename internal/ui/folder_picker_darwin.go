//go:build darwin

package ui

import (
	"os/exec"
	"strings"

	"fyne.io/fyne/v2"
)

// chooseFolder shells out to the macOS "choose folder" dialog via
// osascript, so folder selection uses the real Finder picker instead of
// Fyne's own in-app file browser widget. cb is invoked on the Fyne UI
// thread once the user picks a folder or cancels (path == "").
func chooseFolder(win fyne.Window, cb func(path string, err error)) {
	go func() {
		out, err := exec.Command("osascript", "-e", `POSIX path of (choose folder with prompt "Choose folder")`).Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "User canceled") {
				fyne.Do(func() { cb("", nil) })
				return
			}
			fyne.Do(func() { cb("", err) })
			return
		}
		path := strings.TrimSuffix(strings.TrimSpace(string(out)), "/")
		fyne.Do(func() { cb(path, nil) })
	}()
}

// chooseFileSave shells out to the macOS "choose file name" dialog so saving
// a file uses the native Finder picker. defaultName seeds the filename field.
// cb is invoked on the Fyne UI thread; path == "" means the user cancelled.
func chooseFileSave(win fyne.Window, defaultName string, cb func(path string, err error)) {
	go func() {
		script := `POSIX path of (choose file name with prompt "Save as" default name "` + escapeAppleScript(defaultName) + `")`
		out, err := exec.Command("osascript", "-e", script).Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "User canceled") {
				fyne.Do(func() { cb("", nil) })
				return
			}
			fyne.Do(func() { cb("", err) })
			return
		}
		path := strings.TrimSpace(string(out))
		fyne.Do(func() { cb(path, nil) })
	}()
}

// chooseFileOpen shells out to the macOS "choose file" dialog so opening a
// file uses the native Finder picker. exts limits selectable extensions (e.g.
// "json"); empty allows any. cb runs on the Fyne UI thread; "" means cancel.
func chooseFileOpen(win fyne.Window, exts []string, cb func(path string, err error)) {
	go func() {
		script := `POSIX path of (choose file with prompt "Choose file"`
		if len(exts) > 0 {
			quoted := make([]string, len(exts))
			for i, e := range exts {
				quoted[i] = `"` + escapeAppleScript(e) + `"`
			}
			script += ` of type {` + strings.Join(quoted, ", ") + `}`
		}
		script += `)`
		out, err := exec.Command("osascript", "-e", script).Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "User canceled") {
				fyne.Do(func() { cb("", nil) })
				return
			}
			fyne.Do(func() { cb("", err) })
			return
		}
		path := strings.TrimSpace(string(out))
		fyne.Do(func() { cb(path, nil) })
	}()
}

// escapeAppleScript escapes backslashes and double quotes for safe embedding
// inside an AppleScript string literal.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}
