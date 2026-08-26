package applog

import (
	"os"
	"syscall"
)

// redirectStderr points file descriptor 2 at f - see the darwin
// implementation. Linux uses Dup3 (with no flags, identical to Dup2):
// Dup2 doesn't exist on every Linux architecture Go supports, Dup3 does.
func redirectStderr(f *os.File) error {
	return syscall.Dup3(int(f.Fd()), int(os.Stderr.Fd()), 0)
}
