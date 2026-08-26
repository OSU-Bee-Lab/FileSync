//go:build !darwin && !linux && !windows

package applog

import (
	"errors"
	"os"
)

// redirectStderr has no implementation on platforms outside the three this
// app ships for; Start reports the error and leaves stderr alone.
func redirectStderr(f *os.File) error {
	return errors.New("stderr redirection is not implemented on this platform")
}
