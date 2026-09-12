package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// heldLimit is how many withheld messages are kept.
//
// A gate that fills up silently is a gate that loses messages, and a gate with
// no limit is somewhere a stranger can put unbounded data on your disk. Keeping
// the newest and saying how many were dropped is the honest middle.
const heldLimit = 200

// Held is one message waiting for its recipient to decide.
//
// It is stored unframed, exactly as it arrived. Framing happens at delivery,
// with the level that applies then rather than the one that applied when it was
// held, because the point of holding is that the person had not decided yet.
type Held struct {
	ID       string    `json:"id"`
	At       time.Time `json:"at"`
	Delivery Delivery  `json:"delivery"`
}

// heldStore keeps the messages the hold gate withheld.
//
// It is a file rather than memory because a message somebody has not seen yet
// must survive the connector restarting. Losing it would be the worst of both
// worlds: withheld from the reader and gone before they were asked.
type heldStore struct {
	path string

	mu      sync.Mutex
	Items   []Held `json:"items"`
	Dropped int    `json:"dropped,omitempty"`
}

func newHeldStore(path string) (*heldStore, error) {
	h := &heldStore{path: path}
	if path == "" {
		return h, nil
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, fmt.Errorf("daemon: reading held messages: %w", err)
	}
	if err := json.Unmarshal(raw, h); err != nil {
		// A held file that cannot be read is worth reporting and worth starting
		// over from, because refusing to run would take the whole connector down
		// over messages nobody has seen.
		return h, fmt.Errorf("daemon: parsing held messages, starting empty: %w", err)
	}
	return h, nil
}

// Add withholds a message and returns its identifier.
func (h *heldStore) Add(msg Delivery) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	item := Held{ID: newMessageID()[:12], At: time.Now().UTC(), Delivery: msg}
	h.Items = append(h.Items, item)

	if len(h.Items) > heldLimit {
		h.Dropped += len(h.Items) - heldLimit
		h.Items = h.Items[len(h.Items)-heldLimit:]
	}
	return item.ID, h.persistLocked()
}

// Take removes one message and returns it, so it can be delivered.
func (h *heldStore) Take(id string) (Held, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i, item := range h.Items {
		if item.ID != id {
			continue
		}
		h.Items = append(h.Items[:i], h.Items[i+1:]...)
		return item, h.persistLocked()
	}
	return Held{}, fmt.Errorf("daemon: nothing is held under %q", id)
}

// List returns what is waiting, oldest first.
func (h *heldStore) List() ([]Held, int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]Held, len(h.Items))
	copy(out, h.Items)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, h.Dropped
}

func (h *heldStore) persistLocked() error {
	if h.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return fmt.Errorf("daemon: creating the state directory: %w", err)
	}

	blob, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("daemon: encoding held messages: %w", err)
	}

	f, err := os.CreateTemp(filepath.Dir(h.path), filepath.Base(h.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("daemon: creating a temporary file: %w", err)
	}
	tmp := f.Name()
	if _, err := f.Write(blob); err != nil {
		_ = f.Close()
		os.Remove(tmp)
		return fmt.Errorf("daemon: writing held messages: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, h.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("daemon: installing held messages: %w", err)
	}
	return nil
}
