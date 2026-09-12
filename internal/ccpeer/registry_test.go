package ccpeer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakePlatform lets registry tests control liveness and identity without
// depending on which operating system the test happens to run on.
type fakePlatform struct {
	LocalEndpoint
	alive map[int]bool
}

func (f fakePlatform) GOOS() string                  { return "fake" }
func (f fakePlatform) Verified() bool                { return true }
func (f fakePlatform) ProcessAlive(pid int) bool     { return f.alive[pid] }
func (f fakePlatform) ProcStart(int) (string, error) { return "12345", nil }
func (f fakePlatform) PidDomain() (string, error)    { return "fake:host", nil }
func (f fakePlatform) RequiresAuthLine() bool        { return false }

func writeTestRecord(t *testing.T, dir string, rec Record) {
	t.Helper()
	blob, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshalling a record: %v", err)
	}
	path := filepath.Join(dir, strconv.Itoa(rec.PID)+".json")
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestRegistryListSkipsWhatItShould(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	platform := fakePlatform{alive: map[int]bool{100: true, 300: true}}

	writeTestRecord(t, dir, Record{
		PID: 100, Name: "alive", PeerProtocol: 1, MessagingSocketPath: "/tmp/a",
	})
	writeTestRecord(t, dir, Record{
		PID: 200, Name: "dead", PeerProtocol: 1, MessagingSocketPath: "/tmp/b",
	})
	writeTestRecord(t, dir, Record{
		PID: 300, Name: "future", PeerProtocol: 99, MessagingSocketPath: "/tmp/c",
	})

	// Files that do not match the record name shape are not records at all.
	if err := os.WriteFile(filepath.Join(dir, "notes.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "400.torn.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	reg, err := NewRegistry(platform, dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	got, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(got) != 1 {
		names := make([]string, 0, len(got))
		for _, r := range got {
			names = append(names, r.Name)
		}
		t.Fatalf("List returned %d records %v, want only the live supported one", len(got), names)
	}
	if got[0].Name != "alive" {
		t.Errorf("Name = %q, want %q", got[0].Name, "alive")
	}
}

// TestRegistryDeduplicatesDirectories covers the configuration this project was
// built for: several Claude Code profiles pointing at one session directory
// through a symbolic link. Without deduplication the same session would appear
// once per profile.
func TestRegistryDeduplicatesDirectories(t *testing.T) {
	t.Parallel()

	real := t.TempDir()
	platform := fakePlatform{alive: map[int]bool{100: true}}
	writeTestRecord(t, real, Record{
		PID: 100, Name: "only-once", PeerProtocol: 1, MessagingSocketPath: "/tmp/a",
	})

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this environment does not allow creating symbolic links: %v", err)
	}

	reg, err := NewRegistry(platform, real, link, real)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if n := len(reg.Dirs()); n != 1 {
		t.Fatalf("Dirs returned %d entries %v, want 1", n, reg.Dirs())
	}

	got, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List returned %d records, want 1", len(got))
	}
}

func TestRegistryFind(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	platform := fakePlatform{alive: map[int]bool{1: true, 2: true, 3: true}}

	writeTestRecord(t, dir, Record{PID: 1, Name: "unique", PeerProtocol: 1, MessagingSocketPath: "/tmp/a"})
	writeTestRecord(t, dir, Record{PID: 2, Name: "shared", PeerProtocol: 1, MessagingSocketPath: "/tmp/b"})
	writeTestRecord(t, dir, Record{PID: 3, Name: "shared", PeerProtocol: 1, MessagingSocketPath: "/tmp/c"})

	reg, err := NewRegistry(platform, dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	t.Run("a unique name resolves", func(t *testing.T) {
		rec, err := reg.Find("unique")
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if rec.PID != 1 {
			t.Errorf("PID = %d, want 1", rec.PID)
		}
	})

	t.Run("a shared name is reported rather than guessed", func(t *testing.T) {
		_, err := reg.Find("shared")
		if !errors.Is(err, ErrAmbiguous) {
			t.Fatalf("error = %v, want ErrAmbiguous", err)
		}
	})

	t.Run("an unknown name is not found", func(t *testing.T) {
		_, err := reg.Find("missing")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("error = %v, want ErrNotFound", err)
		}
	})
}

func TestRegistryPublishUpdateWithdraw(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	platform := fakePlatform{alive: map[int]bool{os.Getpid(): true}}

	reg, err := NewRegistry(platform, dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	pub, err := reg.Publish("published", "/tmp/inbox.sock")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if pub.Record.PID != os.Getpid() {
		t.Errorf("the record must carry this process id, got %d", pub.Record.PID)
	}
	if pub.Token == "" {
		t.Error("Publish must return the token it wrote")
	}

	t.Run("the token is readable back through the registry", func(t *testing.T) {
		got, err := reg.Token(os.Getpid())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if got != pub.Token {
			t.Errorf("Token = %q, want %q", got, pub.Token)
		}
	})

	t.Run("update refreshes the status", func(t *testing.T) {
		rec, err := reg.Update(pub, StatusBusy)
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if rec.Status != StatusBusy {
			t.Errorf("Status = %q, want %q", rec.Status, StatusBusy)
		}
		back, err := reg.Get(os.Getpid())
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if back.Status != StatusBusy {
			t.Errorf("the status on disk is %q, want %q", back.Status, StatusBusy)
		}
	})

	t.Run("withdraw removes both files and is safe twice", func(t *testing.T) {
		if err := reg.Withdraw(pub); err != nil {
			t.Fatalf("first Withdraw: %v", err)
		}
		if err := reg.Withdraw(pub); err != nil {
			t.Fatalf("second Withdraw: %v", err)
		}
		for _, f := range []string{pub.RecordPath, pub.KeyPath} {
			if _, err := os.Stat(f); !os.IsNotExist(err) {
				t.Errorf("%s still exists after Withdraw", f)
			}
		}
	})
}

// TestRegistryPublishRefusesAnUnverifiedPlatform is the behaviour that keeps
// macOS honest: rather than writing a record with a guessed start token, the
// registry refuses and the caller degrades.
func TestRegistryPublishRefusesAnUnverifiedPlatform(t *testing.T) {
	t.Parallel()

	reg, err := NewRegistry(unverifiedPlatform{}, t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, err := reg.Publish("x", "/tmp/x.sock"); !errors.Is(err, ErrUnverifiedPlatform) {
		t.Fatalf("error = %v, want ErrUnverifiedPlatform", err)
	}
}

type unverifiedPlatform struct{ fakePlatform }

func (unverifiedPlatform) Verified() bool                { return false }
func (unverifiedPlatform) ProcStart(int) (string, error) { return "", ErrUnverifiedPlatform }
func (unverifiedPlatform) PidDomain() (string, error)    { return "", ErrUnverifiedPlatform }
