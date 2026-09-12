package workspace

import (
	"fmt"
	"os"
	"path/filepath"
)

// lockName is the file the advisory lock is taken on.
//
// It is a file beside the store rather than the store itself, because the store
// is replaced by a rename on every write. A lock held on a file that is about to
// be unlinked protects nothing: the next process opens the new file and takes a
// lock on a different inode, and both believe they hold it.
const lockName = ".lock"

// lock is an advisory lock shared by every process that touches one store.
//
// It exists because two processes genuinely share this file. The relay holds the
// workspace in memory and writes it after every change, and the command line
// writes it directly to create an invitation. Without this, creating an
// invitation while the relay was running did two wrong things at once: the relay
// never saw the new code, because it does not reread, and the next time it
// persisted it overwrote the file and the invitation was gone. Both were silent.
type lock struct{ f *os.File }

// acquire takes the lock for the store at path, blocking until it is free.
//
// Blocking is right here. Every holder keeps it for a single read, change and
// write of a small file, so the wait is short, and a caller that gave up would
// have to report a failure for something that was only ever contention.
func acquire(path string) (*lock, error) {
	if path == "" {
		// A store with no path is held in memory by one process, usually a test.
		// There is nobody to coordinate with.
		return &lock{}, nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("workspace: creating the state directory: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(dir, filepath.Base(path)+lockName),
		os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("workspace: opening the lock file: %w", err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("workspace: locking the store: %w", err)
	}
	return &lock{f: f}, nil
}

// release gives the lock back. The lock file itself is left in place, because
// removing it would let the next process create a different one and hold a lock
// nobody else can see.
func (l *lock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlockFile(l.f)
	_ = l.f.Close()
	l.f = nil
}
