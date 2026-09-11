//go:build !windows

// lock_unix.go — the file lock on Linux, macOS and other Unixes.
//
// See lock_windows.go for the other half and for why this is split at all.
package main

import (
	"os"
	"syscall"
)

// tryLock takes an exclusive lock on f without waiting, reporting whether it
// got one. LOCK_NB is the whole point: a contended run must skip, never
// block, because a blocked status line blanks the bar.
func tryLock(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

// unlock releases the lock. The error is deliberately dropped: this runs from
// a defer on a path that has already done its work, and there is no useful
// response to a failure here. Closing the file releases the lock anyway.
func unlock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
