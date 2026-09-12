//go:build windows

package workspace

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock, waiting for whoever has it.
//
// Windows has no flock. LockFileEx is the equivalent, and without
// LOCKFILE_FAIL_IMMEDIATELY it blocks until the lock is free, which is the
// behaviour the Unix side has. A whole byte range of the maximum size is locked
// rather than a prefix, so it does not matter how large the lock file is or ever
// becomes.
//
// Like flock, the lock belongs to the handle, so a process that dies holding it
// releases it and cannot leave a store nobody can write.
func lockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		^uint32(0), ^uint32(0),
		&overlapped,
	)
}

func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		^uint32(0), ^uint32(0),
		&overlapped,
	)
}
