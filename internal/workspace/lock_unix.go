//go:build linux || darwin

package workspace

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock, waiting for whoever has it.
//
// flock is the right primitive here rather than fcntl locking. It belongs to the
// open file description rather than to the process, so it survives being passed
// around and is released exactly once, when the descriptor closes. That also
// means a process that dies holding it releases it, which is what stops a crash
// from leaving a store nobody can write.
func lockFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err == syscall.EINTR {
			// A signal arrived while waiting. Waiting again is correct.
			continue
		}
		return err
	}
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
