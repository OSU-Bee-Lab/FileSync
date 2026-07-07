package appconfig

import (
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// InstanceLock guards against two copies of FileSync running at once, which
// would let two independent rclone copy operations race over the same
// destination. It wraps an OS-native advisory file lock (flock/LockFileEx),
// which the OS releases automatically on process exit or crash, so there is
// no stale-lock file to clean up.
type InstanceLock struct {
	fl *flock.Flock
}

// AcquireInstanceLock tries to take FileSync's single-instance lock. ok is
// false if another instance already holds it; callers should refuse to
// start rather than proceed unlocked.
func AcquireInstanceLock() (lock *InstanceLock, ok bool, err error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, false, err
	}
	dir = filepath.Join(dir, "FileSync")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}

	fl := flock.New(filepath.Join(dir, "filesync.lock"))
	locked, err := fl.TryLock()
	if err != nil {
		return nil, false, err
	}
	if !locked {
		return nil, false, nil
	}
	return &InstanceLock{fl: fl}, true, nil
}

// Release gives up the lock. It is safe to call on process exit; the OS
// would release it anyway, but this lets a clean shutdown allow an
// immediate relaunch without waiting on OS cleanup timing.
func (l *InstanceLock) Release() error {
	return l.fl.Unlock()
}
