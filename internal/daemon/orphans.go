package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Danielrp551/claudio/internal/ccpeer"
)

// orphanIndex remembers which records this daemon's children published.
//
// It lives outside the Claude Code session directory on purpose. That directory
// belongs to Claude Code, which has its own handling for records it does not
// understand, so this project never adds fields of its own to a record and keeps
// its bookkeeping in its own file instead.
//
// The index exists for one situation: a daemon that dies without running its
// shutdown, leaving records behind. On the next start, Sweep removes them.
type orphanIndex struct {
	path string

	mu      sync.Mutex
	entries map[int]orphanEntry
}

type orphanEntry struct {
	PID         int       `json:"pid"`
	Name        string    `json:"name"`
	SessionsDir string    `json:"sessionsDir"`
	SpawnedAt   time.Time `json:"spawnedAt"`
}

func newOrphanIndex(path string) (*orphanIndex, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: creating the state directory: %w", err)
	}
	idx := &orphanIndex{path: path, entries: map[int]orphanEntry{}}
	if err := idx.load(); err != nil {
		return nil, err
	}
	return idx, nil
}

func (o *orphanIndex) load() error {
	raw, err := os.ReadFile(o.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("daemon: reading the ghost index: %w", err)
	}

	var entries []orphanEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		// A corrupt index is not worth failing a start over. The cost of losing
		// it is a few inert records that Claude Code already ignores, and the
		// cost of refusing to start is the whole tool.
		//
		//nolint:nilerr // losing this file is recoverable, refusing to start is not
		return nil
	}
	for _, e := range entries {
		o.entries[e.PID] = e
	}
	return nil
}

func (o *orphanIndex) persistLocked() error {
	entries := make([]orphanEntry, 0, len(o.entries))
	for _, e := range o.entries {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].PID < entries[j].PID })

	blob, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("daemon: encoding the ghost index: %w", err)
	}

	tmp := o.path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return fmt.Errorf("daemon: writing the ghost index: %w", err)
	}
	if err := os.Rename(tmp, o.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("daemon: installing the ghost index: %w", err)
	}
	return nil
}

// Track records a child before it is told to publish anything.
//
// The order is deliberate. The process id is known the moment the child starts,
// so writing the index first closes the window in which a ghost could publish a
// record and die before it had a chance to report what it published.
func (o *orphanIndex) Track(pid int, name, sessionsDir string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.entries[pid] = orphanEntry{
		PID:         pid,
		Name:        name,
		SessionsDir: sessionsDir,
		SpawnedAt:   time.Now(),
	}
	return o.persistLocked()
}

// Forget drops a child that stopped cleanly and removed its own files.
func (o *orphanIndex) Forget(pid int) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	delete(o.entries, pid)
	return o.persistLocked()
}

// Sweep removes the records of tracked children that are no longer running.
//
// The only condition for removing a file is that its process is dead. That is
// what makes this safe against process id reuse: if the number now belongs to a
// live process, whether one of ours or a real Claude Code session, it is left
// strictly alone.
//
// It returns how many records it removed, which is worth surfacing because a
// non zero count means the previous run did not shut down cleanly.
func (o *orphanIndex) Sweep(platform ccpeer.LocalEndpoint) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	var removed int
	var problems []error

	for pid, e := range o.entries {
		if platform.ProcessAlive(pid) {
			continue
		}

		record := filepath.Join(e.SessionsDir, strconv.Itoa(pid)+".json")
		if err := os.Remove(record); err != nil && !errors.Is(err, os.ErrNotExist) {
			problems = append(problems, fmt.Errorf("removing %s: %w", record, err))
		} else if err == nil {
			removed++
		}

		keys, _ := filepath.Glob(filepath.Join(e.SessionsDir, strconv.Itoa(pid)+".*.key"))
		for _, k := range keys {
			if err := os.Remove(k); err != nil && !errors.Is(err, os.ErrNotExist) {
				problems = append(problems, fmt.Errorf("removing %s: %w", k, err))
			}
		}

		delete(o.entries, pid)
	}

	if err := o.persistLocked(); err != nil {
		problems = append(problems, err)
	}
	return removed, errors.Join(problems...)
}

// Tracked returns the entries currently held, for diagnostics.
func (o *orphanIndex) Tracked() []orphanEntry {
	o.mu.Lock()
	defer o.mu.Unlock()

	out := make([]orphanEntry, 0, len(o.entries))
	for _, e := range o.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}
