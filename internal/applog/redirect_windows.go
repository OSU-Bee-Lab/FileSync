package applog

import (
	"os"

	"golang.org/x/sys/windows"
)

// redirectStderr repoints the process's standard error handle at f, the
// Windows equivalent of the dup2 the unix builds do.
//
// It is best-effort in a way the unix versions aren't: the Go runtime
// caches the standard handles at startup, so a panic stack may still go to
// the handle the process started with. For this app that's usually no
// handle at all (a windowsgui build has no console), which is exactly the
// case where nothing was being captured before anyway - everything routed
// through os.Stderr, including rclone's and Fyne's logging, does reach the
// file.
func redirectStderr(f *os.File) error {
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())); err != nil {
		return err
	}
	os.Stderr = f
	return nil
}
