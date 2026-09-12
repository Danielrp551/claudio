package workspace

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Danielrp551/claudio/internal/identity"
)

func newIdentity(t *testing.T) identity.Public {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating an identity: %v", err)
	}
	return id.Public()
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "workspace.json"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return s
}

func TestCreateWorkspace(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	owner := newIdentity(t)

	w, member, err := s.CreateWorkspace("acme", "Acme", "daniel", owner)
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if w.OwnerID != member.ID {
		t.Error("the workspace does not point at its owner")
	}
	if member.Role != RoleOwner {
		t.Errorf("Role = %q, want %q", member.Role, RoleOwner)
	}

	if _, _, err := s.CreateWorkspace("ACME", "again", "somebody", newIdentity(t)); !errors.Is(err, ErrExists) {
		t.Fatalf("creating a duplicate slug returned %v, want ErrExists", err)
	}
}

// TestOwningAWorkspaceGrantsNoExtraTrust is a regression test.
//
// An earlier version gave the owner the highest trust level. Because a member's
// level is proposed to every other machine, that quietly granted the owner
// maximum trust on everybody's computer, which is the one thing ADR-0006 says
// must not be possible. Owning a workspace is administration, not trust.
func TestOwningAWorkspaceGrantsNoExtraTrust(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	w, owner, err := s.CreateWorkspace("acme", "Acme", "daniel", newIdentity(t))
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	if owner.Trust != w.DefaultTrust {
		t.Errorf("the owner is proposed at %q while everybody else gets %q",
			owner.Trust, w.DefaultTrust)
	}
	if owner.Trust == "peer" {
		t.Error("the owner is proposed at the highest level, which grants it on every member's machine")
	}
	if owner.Role != RoleOwner {
		t.Errorf("the owner lost its role, got %q", owner.Role)
	}
}

// TestInvitationIsAVoucherNotACredential covers the property the security model
// rests on. Two people redeeming the same code get two separate memberships with
// separate keys, so revoking one leaves the other alone.
func TestInvitationIsAVoucherNotACredential(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	w, owner, err := s.CreateWorkspace("acme", "Acme", "daniel", newIdentity(t))
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	code, inv, err := s.CreateInvitation(w.ID, owner.ID, time.Hour, 5)
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if inv.CodeHash == code {
		t.Fatal("the invitation stored the code itself rather than its hash")
	}

	luisKey := newIdentity(t)
	anaKey := newIdentity(t)

	luis, _, err := s.Redeem(code, "luis", luisKey)
	if err != nil {
		t.Fatalf("Redeem by luis: %v", err)
	}
	ana, _, err := s.Redeem(code, "ana", anaKey)
	if err != nil {
		t.Fatalf("Redeem by ana: %v", err)
	}

	if luis.ID == ana.ID {
		t.Fatal("two redemptions produced one membership")
	}

	if err := s.Revoke(luis.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if _, err := s.MemberBySigning(w.ID, luisKey.Signing); !errors.Is(err, ErrRevoked) {
		t.Errorf("the revoked member resolves to %v, want ErrRevoked", err)
	}
	if _, err := s.MemberBySigning(w.ID, anaKey.Signing); err != nil {
		t.Errorf("revoking one member broke another: %v", err)
	}
}

func TestInvitationLimits(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	w, owner, _ := s.CreateWorkspace("acme", "Acme", "daniel", newIdentity(t))

	t.Run("a spent invitation cannot be redeemed", func(t *testing.T) {
		code, _, err := s.CreateInvitation(w.ID, owner.ID, time.Hour, 1)
		if err != nil {
			t.Fatalf("CreateInvitation: %v", err)
		}
		if _, _, err := s.Redeem(code, "first", newIdentity(t)); err != nil {
			t.Fatalf("first Redeem: %v", err)
		}
		if _, _, err := s.Redeem(code, "second", newIdentity(t)); !errors.Is(err, ErrInvitationUnusable) {
			t.Fatalf("second Redeem returned %v, want ErrInvitationUnusable", err)
		}
	})

	t.Run("an expired invitation cannot be redeemed", func(t *testing.T) {
		// A lifetime short enough to have passed by the time it is redeemed,
		// without making the test wait for anything.
		code, _, err := s.CreateInvitation(w.ID, owner.ID, time.Nanosecond, 0)
		if err != nil {
			t.Fatalf("CreateInvitation: %v", err)
		}
		if _, _, err := s.Redeem(code, "late", newIdentity(t)); !errors.Is(err, ErrInvitationUnusable) {
			t.Fatalf("Redeem returned %v, want ErrInvitationUnusable", err)
		}
	})

	t.Run("a negative lifetime is refused rather than treated as no expiry", func(t *testing.T) {
		if _, _, err := s.CreateInvitation(w.ID, owner.ID, -time.Minute, 0); err == nil {
			t.Fatal("CreateInvitation accepted a negative lifetime")
		}
	})

	t.Run("an unknown code cannot be redeemed", func(t *testing.T) {
		if _, _, err := s.Redeem("NOPE-NOPE-NOPE", "stranger", newIdentity(t)); !errors.Is(err, ErrInvitationUnusable) {
			t.Fatalf("Redeem returned %v, want ErrInvitationUnusable", err)
		}
	})
}

// TestRedeemIsForgivingAboutFormatting matters because people read these codes
// out loud and type them by hand.
func TestRedeemIsForgivingAboutFormatting(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	w, owner, _ := s.CreateWorkspace("acme", "Acme", "daniel", newIdentity(t))
	code, _, err := s.CreateInvitation(w.ID, owner.ID, time.Hour, 0)
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}

	messy := "  " + lower(code) + "  "
	if _, _, err := s.Redeem(messy, "luis", newIdentity(t)); err != nil {
		t.Fatalf("Redeem with different case and spacing failed: %v", err)
	}
}

func lower(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + 32
		}
	}
	return string(out)
}

func TestRevokingRemovesTheSessionsAtOnce(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	w, owner, _ := s.CreateWorkspace("acme", "Acme", "daniel", newIdentity(t))
	code, _, _ := s.CreateInvitation(w.ID, owner.ID, time.Hour, 0)
	luis, _, err := s.Redeem(code, "luis", newIdentity(t))
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	err = s.ReplaceSessions(luis.ID, Machine{ID: "m1", Hostname: "thinkpad", OS: "linux"},
		[]Session{{ID: "s1", Name: "api", Status: "idle"}})
	if err != nil {
		t.Fatalf("ReplaceSessions: %v", err)
	}
	if n := len(s.Roster(w.ID, owner.ID)); n != 1 {
		t.Fatalf("the roster holds %d sessions, want 1", n)
	}

	if err := s.Revoke(luis.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if n := len(s.Roster(w.ID, owner.ID)); n != 0 {
		t.Errorf("a revoked member still has %d sessions in the roster", n)
	}
}

func TestOwnerCannotBeRevoked(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	_, owner, _ := s.CreateWorkspace("acme", "Acme", "daniel", newIdentity(t))

	if err := s.Revoke(owner.ID); err == nil {
		t.Fatal("revoking the owner was allowed")
	}
}

func TestRosterExcludesYourOwnSessions(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	w, owner, _ := s.CreateWorkspace("acme", "Acme", "daniel", newIdentity(t))
	code, _, _ := s.CreateInvitation(w.ID, owner.ID, time.Hour, 0)
	luis, _, _ := s.Redeem(code, "luis", newIdentity(t))

	_ = s.ReplaceSessions(owner.ID, Machine{ID: "mine", Hostname: "desktop", OS: "windows"},
		[]Session{{ID: "own", Name: "herramientas", Status: "busy"}})
	_ = s.ReplaceSessions(luis.ID, Machine{ID: "theirs", Hostname: "thinkpad", OS: "linux"},
		[]Session{{ID: "api", Name: "api", Status: "idle"}})

	roster := s.Roster(w.ID, owner.ID)
	if len(roster) != 1 {
		t.Fatalf("the roster holds %d sessions, want only the other person's", len(roster))
	}
	if roster[0].Name != "api" {
		t.Errorf("the roster holds %q, want the other person's session", roster[0].Name)
	}
}

func TestReplaceSessionsReplacesRatherThanAccumulates(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	w, owner, _ := s.CreateWorkspace("acme", "Acme", "daniel", newIdentity(t))
	code, _, _ := s.CreateInvitation(w.ID, owner.ID, time.Hour, 0)
	luis, _, _ := s.Redeem(code, "luis", newIdentity(t))

	machine := Machine{ID: "m1", Hostname: "thinkpad", OS: "linux"}

	_ = s.ReplaceSessions(luis.ID, machine, []Session{
		{ID: "a", Name: "api"}, {ID: "b", Name: "front"},
	})
	_ = s.ReplaceSessions(luis.ID, machine, []Session{{ID: "a", Name: "api"}})

	if n := len(s.Roster(w.ID, owner.ID)); n != 1 {
		t.Errorf("the roster holds %d sessions, want 1 after a machine narrowed what it exposes", n)
	}
}

func TestStoreSurvivesAReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "workspace.json")

	first, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	w, owner, err := first.CreateWorkspace("acme", "Acme", "daniel", newIdentity(t))
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	code, _, _ := first.CreateInvitation(w.ID, owner.ID, time.Hour, 0)

	second, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if _, err := second.WorkspaceBySlug("acme"); err != nil {
		t.Fatalf("the workspace did not survive a reopen: %v", err)
	}
	if _, _, err := second.Redeem(code, "luis", newIdentity(t)); err != nil {
		t.Fatalf("the invitation did not survive a reopen: %v", err)
	}
}
