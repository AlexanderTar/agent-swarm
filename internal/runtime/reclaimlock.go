package runtime

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockReclaim takes an exclusive flock on <home>/reclaim.lock, blocking until
// any other reclaim pass (the daemon's loop or `swarm cleanup`) finishes. The
// kernel drops it if the holder dies.
func lockReclaim(home string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(home, "reclaim.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil // closing releases the lock
}
