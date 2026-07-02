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
