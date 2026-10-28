//go:build !windows

package privacy

import (
	"os"
	"syscall"
)

func tryConfigLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func unlockConfig(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
