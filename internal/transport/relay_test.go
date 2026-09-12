package transport

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielrp551/claudio/internal/daemon"
	"github.com/danielrp551/claudio/internal/identity"
	"github.com/danielrp551/claudio/internal/relay"
	"github.com/danielrp551/claudio/internal/workspace"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fixture struct {
	t      *testing.T
	url    string
	store  *workspace.Store
	alice  *identity.Identity
	bob    *identity.Identity
	server *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	store, err := workspace.OpenStore(filepath.Join(t.TempDir(), "workspace.json"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	alice, err := identity.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	bob, err := identity.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	ws, owner, err := store.CreateWorkspace("acme", "Acme", "alice", alice.Public())
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	code, _, err := store.CreateInvitation(ws.ID, owner.ID, time.Hour, 0)
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if _, _, err := store.Redeem(code, "bob", bob.Public()); err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	server := httptest.NewServer(relay.NewServer(store, quietLogger()).Handler())
	t.Cleanup(server.Close)

	return &fixture{
		t:      t,
		url:    "ws" + strings.TrimPrefix(server.URL, "http") + "/connect",
		store:  store,
		alice:  alice,
		bob:    bob,
		server: server,
	}
}

func (f *fixture) dial(t *testing.T, id *identity.Identity, person, machine string) *RelayClient {
	t.Helper()

	c, err := Dial(context.Background(), Options{
		URL:       f.url,
		Workspace: "acme",
		Identity:  id,
		Person:    person,
		MachineID: machine,
		Logger:    quietLogger(),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return c
}

// waitForRoster waits until a client can see a session with the given name.
func waitForRoster(t *testing.T, c *RelayClient, name string) daemon.RemoteSession {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range c.Roster() {
			if s.Session == name {
				return s
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the session %q never appeared in the roster, it holds %+v", name, c.Roster())
	return daemon.RemoteSession{}
}

// TestMessageTravelsSealedBetweenMembers is the end to end path: two members of
// one workspace, a real relay, a real handshake, and a real sealed envelope.
func TestMessageTravelsSealedBetweenMembers(t *testing.T) {
	f := newFixture(t)

	alice := f.dial(t, f.alice, "alice", "alice-machine")
	bob := f.dial(t, f.bob, "bob", "bob-machine")

	bob.Expose([]daemon.ExposedSession{{ID: "bob-api", Name: "api", Status: "idle"}})
	alice.Expose([]daemon.ExposedSession{{ID: "alice-web", Name: "web", Status: "idle"}})

	target := waitForRoster(t, alice, "api")

	const text = "el schema cambio, tenant_id es la columna nueva"

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := alice.Send(ctx, daemon.Outbound{
		To:          target.ID,
		Text:        text,
		FromSession: "alice-web-session",
		FromMode:    "prompting",
		MsgID:       "m-1",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-bob.Deliveries():
		if got.Text != text {
			t.Errorf("Text = %q, want %q", got.Text, text)
		}
		if got.ToSession != "bob-api" {
			t.Errorf("ToSession = %q, want %q", got.ToSession, "bob-api")
		}
		if got.From.Person != "alice" {
			t.Errorf("the sender is %q, want alice", got.From.Person)
		}
		if got.FromMode != "prompting" {
			t.Errorf("FromMode = %q, want prompting", got.FromMode)
		}
		if got.MsgID != "m-1" {
			t.Errorf("MsgID = %q, want m-1", got.MsgID)
		}
	case <-ctx.Done():
		t.Fatal("the message never arrived")
	}
}

// TestTheRelayNeverSeesPlaintext seals a message the same way the client does
// and checks the bytes that would cross the relay. A relay operator reading the
// wire must not be able to find the message in it.
func TestTheRelayNeverSeesPlaintext(t *testing.T) {
	t.Parallel()

	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

	const secret = "no le digas a nadie"
	payload := []byte(`{"text":"` + secret + `"}`)

	env, err := alice.Seal(bob.Public(), payload)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	for name, field := range map[string][]byte{
		"ciphertext": env.Ciphertext,
		"nonce":      env.Nonce,
		"ephemeral":  env.Ephemeral,
		"sender":     env.Sender,
		"signature":  env.Signature,
	} {
		if bytes.Contains(field, []byte(secret)) {
			t.Errorf("the %s of an envelope carries the plaintext", name)
		}
	}
}

// TestSendRefusesAnUnknownSession covers the case where a session went away
// between a roster update and a send.
func TestSendRefusesAnUnknownSession(t *testing.T) {
	f := newFixture(t)
	alice := f.dial(t, f.alice, "alice", "alice-machine")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := alice.Send(ctx, daemon.Outbound{To: "nobody", Text: "hola", MsgID: "m-1"})
	if err == nil {
		t.Fatal("Send accepted a session nobody reaches")
	}
}

// TestAStrangerCannotConnect checks that membership, not merely knowing the
// workspace name, is what gets a connector in.
func TestAStrangerCannotConnect(t *testing.T) {
	f := newFixture(t)

	stranger, err := identity.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	c, err := Dial(context.Background(), Options{
		URL:       f.url,
		Workspace: "acme",
		Identity:  stranger,
		Person:    "stranger",
		MachineID: "stranger-machine",
		Logger:    quietLogger(),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	// A stranger's connection is refused during the handshake, so no roster
	// ever arrives.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(c.Roster()) != 0 {
			t.Fatal("a stranger received a roster")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestRevokedMemberLosesAccess is the property that makes an invitation safe to
// hand out: taking somebody out is immediate and affects nobody else.
func TestRevokedMemberLosesAccess(t *testing.T) {
	f := newFixture(t)

	bobMember, err := f.store.MemberBySigning(mustWorkspaceID(t, f), f.bob.Public().Signing)
	if err != nil {
		t.Fatalf("MemberBySigning: %v", err)
	}
	if err := f.store.Revoke(bobMember.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	c, err := Dial(context.Background(), Options{
		URL:       f.url,
		Workspace: "acme",
		Identity:  f.bob,
		Person:    "bob",
		MachineID: "bob-machine",
		Logger:    quietLogger(),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(c.Roster()) != 0 {
			t.Fatal("a revoked member received a roster")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func mustWorkspaceID(t *testing.T, f *fixture) string {
	t.Helper()
	ws, err := f.store.WorkspaceBySlug("acme")
	if err != nil {
		t.Fatalf("WorkspaceBySlug: %v", err)
	}
	return ws.ID
}
