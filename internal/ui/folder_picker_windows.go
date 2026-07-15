//go:build windows

package ui

import (
	"os/exec"
	"strings"
	"syscall"

	"fyne.io/fyne/v2"
)

// runPowerShell runs a PowerShell script hidden (no flashing console window)
// and returns its trimmed stdout.
func runPowerShell(script string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// chooseFolder shows Windows' native File Explorer-style folder picker.
// WinForms' own FolderBrowserDialog is stuck with the old tree-view style, so
// this instead drives OpenFileDialog in folder-picking mode (a well-known
// trick: point it at a fake filename and take the containing directory of
// whatever the user "opens"), which gets the modern Explorer dialog. cb is
// invoked on the Fyne UI thread once the user picks a folder or cancels
// (path == "").
func chooseFolder(win fyne.Window, cb func(path string, err error)) {
	go func() {
		const script = `
Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.ValidateNames = $false
$d.CheckFileExists = $false
$d.CheckPathExists = $true
$d.FileName = "Select Folder"
$d.Title = "Choose folder"
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
	Split-Path $d.FileName
}
`
		path, err := runPowerShell(script)
		fyne.Do(func() { cb(path, err) })
	}()
}

// chooseFileSave shows Windows' native SaveFileDialog (already the modern
// File Explorer-style dialog). defaultName seeds the filename field. cb is
// invoked on the Fyne UI thread; path == "" means the user cancelled.
func chooseFileSave(win fyne.Window, defaultName string, cb func(path string, err error)) {
	go func() {
		script := `
Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.SaveFileDialog
$d.FileName = "` + escapePowerShell(defaultName) + `"
$d.Title = "Save as"
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
	$d.FileName
}
`
		path, err := runPowerShell(script)
		fyne.Do(func() { cb(path, err) })
	}()
}

// chooseFileOpen shows Windows' native OpenFileDialog (already the modern
// File Explorer-style dialog). exts limits selectable extensions (e.g.
// "json"); empty allows any. cb runs on the Fyne UI thread; "" means cancel.
func chooseFileOpen(win fyne.Window, exts []string, cb func(path string, err error)) {
	go func() {
		filter := "All files (*.*)|*.*"
		if len(exts) > 0 {
			patterns := make([]string, len(exts))
			for i, e := range exts {
				patterns[i] = "*." + e
			}
			joined := strings.Join(patterns, ";")
			filter = strings.ToUpper(strings.Join(exts, "/")) + " files (" + joined + ")|" + joined
		}
		script := `
Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Title = "Choose file"
$d.Filter = "` + escapePowerShell(filter) + `"
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
	$d.FileName
}
`
		path, err := runPowerShell(script)
		fyne.Do(func() { cb(path, err) })
	}()
}

// escapePowerShell escapes double quotes and backticks for safe embedding
// inside a PowerShell double-quoted string literal.
func escapePowerShell(s string) string {
	s = strings.ReplaceAll(s, "`", "``")
	return strings.ReplaceAll(s, `"`, "`\"")
}
