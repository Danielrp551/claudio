package workspace

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Danielrp551/claudio/internal/identity"
)

// newSharedStore opens a second handle on the same file, the way a running relay
// and a command line invocation hold one workspace between them.
func newSharedStore(t *testing.T, path string) *Store {
	t.Helper()

	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return s
}

func mustIdentity(t *testing.T) *identity.Identity {
	t.Helper()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return id
}

// TestAnInvitationSurvivesTheOtherProcess is the regression test for a failure
// that lost data without a word.
//
// The relay holds the workspace in memory and writes it after every change. The
// command line writes the same file directly to create an invitation. Before the
// file lock, creating one while the relay was running did two wrong things at
// once: the relay went on refusing the new code, because it never rereads, and
// the next time it persisted anything it overwrote the file and the invitation
// was gone. Three invitations on disk became two, silently.
func TestAnInvitationSurvivesTheOtherProcess(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "workspace.json")

	// The relay, holding the workspace since it started.
	relay := newSharedStore(t, path)
	owner := mustIdentity(t)
	ws, ownerMember, err := relay.CreateWorkspace("acme", "Acme", "ana", owner.Public())
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// The command line, a separate process, adding an invitation.
	cli := newSharedStore(t, path)
	code, _, err := cli.CreateInvitation(ws.ID, ownerMember.ID, time.Hour, 1)
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}

	// The relay persists again, for its own reasons. This is what used to
	// destroy the invitation.
	if err := relay.ReplaceSessions(ownerMember.ID, Machine{ID: "m1"}, nil); err != nil {
		t.Fatalf("ReplaceSessions: %v", err)
	}

	// The invitation has to still be there, and the relay has to honour it.
	joiner := mustIdentity(t)
	member, joined, err := relay.Redeem(code, "luis", joiner.Public())
	if err != nil {
		t.Fatalf("the relay refused an invitation the command line created: %v", err)
	}
	if joined.ID != ws.ID {
		t.Errorf("joined %s, want %s", joined.ID, ws.ID)
	}
	if member.Person != "luis" {
		t.Errorf("the member is %q, want luis", member.Person)
	}

	// And a third handle, opened fresh, has to agree with both of them.
	third := newSharedStore(t, path)
	if n := len(third.ListMembers(ws.ID)); n != 2 {
		t.Errorf("a fresh handle sees %d members, want 2", n)
	}
}

// TestAReaderSeesWhatAnotherProcessWrote covers the other half. A reader that
// answers from a copy loaded at startup is a reader that is wrong.
func TestAReaderSeesWhatAnotherProcessWrote(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "workspace.json")

	first := newSharedStore(t, path)
	owner := mustIdentity(t)
	ws, ownerMember, err := first.CreateWorkspace("acme", "Acme", "ana", owner.Public())
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// Opened before the change, the way a long running relay is.
	watcher := newSharedStore(t, path)
	if n := len(watcher.ListMembers(ws.ID)); n != 1 {
		t.Fatalf("the watcher starts with %d members, want 1", n)
	}

	code, _, err := first.CreateInvitation(ws.ID, ownerMember.ID, time.Hour, 1)
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	joiner := mustIdentity(t)
	if _, _, err := first.Redeem(code, "luis", joiner.Public()); err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	if n := len(watcher.ListMembers(ws.ID)); n != 2 {
		t.Errorf("the watcher still sees %d members, so it is answering from a stale copy", n)
	}
	if _, err := watcher.WorkspaceBySlug("acme"); err != nil {
		t.Errorf("WorkspaceBySlug: %v", err)
	}
}

// TestConcurrentWritersDoNotLoseEachOther is the property the lock exists for,
// stated as a race rather than as a sequence.
func TestConcurrentWritersDoNotLoseEachOther(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "workspace.json")

	setup := newSharedStore(t, path)
	owner := mustIdentity(t)
	ws, ownerMember, err := setup.CreateWorkspace("acme", "Acme", "ana", owner.Public())
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// Each writer is its own handle, which is what a separate process is.
	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)

	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			store := newSharedStore(t, path)
			if _, _, err := store.CreateInvitation(ws.ID, ownerMember.ID, time.Hour, 1); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("CreateInvitation: %v", err)
	}

	final := newSharedStore(t, path)
	final.mu.Lock()
	got := len(final.Invitations)
	final.mu.Unlock()

	if got != writers {
		t.Errorf("%d invitations survived out of %d, so writers overwrote each other", got, writers)
	}
}
