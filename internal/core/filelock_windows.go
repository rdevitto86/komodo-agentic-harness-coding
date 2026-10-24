//go:build windows

package core

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2
	errorLockViolation      = syscall.Errno(33)
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// tryLock locks one byte far past the end of the file without blocking, so reads of the file never collide with the lock;
// busy reports that another holder has it.
func tryLock(f *os.File, shared bool) (busy bool, err error) {
	flags := uintptr(lockfileFailImmediately)
	if !shared {
		flags |= lockfileExclusiveLock
	}

	ol := lockRegion()
	r, _, e := procLockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(ol)))
	if r != 0 {
		return false, nil
	}
	if errors.Is(e, errorLockViolation) {
		return true, nil
	}

	return false, e
}

// unlockFile drops the lock held on f.
func unlockFile(f *os.File) error {
	ol := lockRegion()
	r, _, e := procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(ol)))
	if r == 0 {
		return e
	}

	return nil
}

// lockRegion is the offset of the locked byte.
func lockRegion() *syscall.Overlapped {
	return &syscall.Overlapped{OffsetHigh: 0x7fffffff}
}
