//go:build windows

// lock_windows.go — the file lock on Windows.
//
// # WHY THIS IS SPLIT IN TWO
//
// Several copies of this program run at once: Claude Code fires the status
// line on close to every turn, across every open session, without waiting for
// the previous run. Two copies appending to the same history file produced
// three identical records on first install, before any lock existed.
//
// Unix has flock. Windows has LockFileEx, which is a different function with
// different arguments and no Go equivalent in common. Rather than spread that
// difference through history.go and watch.go, both call tryLock and unlock and
// never learn which platform answered. The build tags mean the compiler
// includes exactly one of these two files.
//
// # WHY THE DLL CALL RATHER THAN x/sys
//
// `syscall.LockFileEx` does NOT exist in Go's standard library on Windows —
// it is in golang.org/x/sys/windows. Taking that would be this program's only
// dependency and would break design rule 4 (standard library only, static
// binary, no runtime dependencies), which the README states publicly.
//
// kernel32.dll is where x/sys gets it from as well, so calling it directly
// costs about twenty lines and keeps the rule. That is the trade, made
// deliberately.
package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	// Fail rather than wait — the Windows spelling of LOCK_NB.
	lockfileFailImmediately = 0x00000001
	// Exclusive rather than shared — the Windows spelling of LOCK_EX.
	lockfileExclusiveLock = 0x00000002
)

// tryLock takes an exclusive lock on f without waiting, reporting whether it
// got one.
//
// Windows locks byte ranges rather than whole files, so this locks the first
// byte and nothing else ever locks a different range. The file need not be
// that long: a range lock does not require the bytes to exist.
func tryLock(f *os.File) bool {
	var ol syscall.Overlapped
	ret, _, _ := procLockFileEx.Call(
		f.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0, // reserved, must be zero
		1, // bytes to lock, low word
		0, // bytes to lock, high word
		uintptr(unsafe.Pointer(&ol)),
	)
	return ret != 0
}

// unlock releases the lock, dropping any error for the same reason the Unix
// half does: it runs from a defer after the work is done, and closing the
// handle releases the lock regardless.
func unlock(f *os.File) {
	var ol syscall.Overlapped
	_, _, _ = procUnlockFileEx.Call(
		f.Fd(),
		0, // reserved, must be zero
		1, // bytes to unlock, low word
		0, // bytes to unlock, high word
		uintptr(unsafe.Pointer(&ol)),
	)
}
