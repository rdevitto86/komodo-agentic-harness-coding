//go:build unix

package core

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes a shared or exclusive advisory lock without blocking; busy reports that another holder has it.
func tryLock(f *os.File, shared bool) (busy bool, err error) {
	how := syscall.LOCK_EX
	if shared {
		how = syscall.LOCK_SH
	}

	for {
		err = syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}

	return false, err
}

// unlockFile drops the advisory lock held on f.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
