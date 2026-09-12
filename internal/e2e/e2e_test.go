// Package e2e holds the acceptance test for version one.
//
// It exists as its own package because it needs the daemon, the relay, and the
// transport at once, and the transport imports the daemon for the types the
// daemon declares. Putting the test here keeps that direction clean.
package e2e

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danielrp551/claudio/internal/ccpeer"
	"github.com/danielrp551/claudio/internal/daemon"
	"github.com/danielrp551/claudio/internal/identity"
	"github.com/danielrp551/claudio/internal/relay"
	"github.com/danielrp551/claudio/internal/transport"
	"github.com/danielrp551/claudio/internal/trust"
	"github.com/danielrp551/claudio/internal/workspace"
)

var claudioBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "claudio-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	claudioBinary = filepath.Join(dir, "claudio")
	if runtime.GOOS == "windows" {
		claudioBinary += ".exe"
	}
	build := exec.Command("go", "build", "-o", claudioBinary, "../../cmd/claudio")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building the binary:", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// machine is one person's computer: their own session registry, their own
// connector, and a stand in for a Claude Code session running on it.
type machine struct {
	t        *testing.T
	person   string
	sessions string
	platform ccpeer.LocalEndpoint
	registry *ccpeer.Registry

	// The stand in for a Claude Code session.
	sessionName string
	inbox       *ccpeer.Inbox
	inboxPath   string

	client *transport.RelayClient
	daemon *daemon.Daemon
	done   chan error
	cancel context.CancelFunc
}

func newMachine(t *testing.T, person, sessionName, relayURL string, id *identity.Identity) *machine {
	t.Helper()

	platform := ccpeer.Platform()
	if !platform.Verified() {
		t.Skipf("%s is not a verified platform", platform.GOOS())
	}

	m := &machine{
		t:           t,
		person:      person,
		sessions:    t.TempDir(),
		platform:    platform,
		sessionName: sessionName,
	}

	reg, err := ccpeer.NewRegistry(platform, m.sessions)
	if err != nil {
		t.Fatalf("%s: NewRegistry: %v", person, err)
	}
	m.registry = reg

	// Stand in for a Claude Code session: publish a record and listen on the
	// endpoint it names, which is exactly what a real session does.
	m.inboxPath, err = platform.NewInboxPath()
	if err != nil {
		t.Fatalf("%s: NewInboxPath: %v", person, err)
	}
	m.inbox, err = ccpeer.Listen(platform, m.inboxPath, "", quiet())
	if err != nil {
		t.Fatalf("%s: Listen: %v", person, err)
	}
	pub, err := reg.Publish(sessionName, m.inboxPath)
	if err != nil {
		t.Fatalf("%s: Publish: %v", person, err)
	}
	m.inbox.SetToken(pub.Token)
	t.Cleanup(func() {
		_ = m.inbox.Close()
		_ = reg.Withdraw(pub)
	})

	m.client, err = transport.Dial(context.Background(), transport.Options{
		URL:       relayURL,
		Workspace: "acme",
		Identity:  id,
		Person:    person,
		MachineID: person + "-machine",
		Logger:    quiet(),
	})
	if err != nil {
		t.Fatalf("%s: Dial: %v", person, err)
	}

	state := t.TempDir()
	m.daemon, err = daemon.New(daemon.Options{
		Platform:     platform,
		SessionsDirs: []string{m.sessions},
		StatePath:    filepath.Join(state, "ghosts.json"),
		StatusPath:   filepath.Join(state, "status.json"),
		Executable:   claudioBinary,
		Workspace:    "acme",
		MachineID:    person + "-machine",
		Policy:       daemon.Policy{Mode: daemon.ModeAuto, MaxGhosts: 4},
		Transport:    m.client,
		Exposes:      func(name string) bool { return name == sessionName },
		Logger:       quiet(),
		PollInterval: 150 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("%s: New: %v", person, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan error, 1)
	go func() { m.done <- m.daemon.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-m.done:
			if err != nil {
				t.Errorf("%s: Run: %v", person, err)
			}
		case <-time.After(30 * time.Second):
			t.Errorf("%s: the connector did not stop", person)
		}
		_ = m.client.Close()
	})

	return m
}

// waitForPeer waits until this machine has published a native peer with the
// given name, which is the moment a remote session becomes addressable.
func (m *machine) waitForPeer(name string, within time.Duration) ccpeer.Record {
	m.t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if rec, err := m.registry.Find(name); err == nil {
			return rec
		}
		time.Sleep(100 * time.Millisecond)
	}

	records, _ := m.registry.List()
	names := make([]string, 0, len(records))
	for _, r := range records {
		names = append(names, r.Name)
	}
	m.t.Fatalf("%s: the peer %q never appeared, the registry holds %v", m.person, name, names)
	return ccpeer.Record{}
}

// write sends a frame the way a Claude Code session would.
func (m *machine) write(target ccpeer.Record, text string) {
	m.t.Helper()

	frame, err := ccpeer.NewFrame(ccpeer.FrameOptions{
		Text:        text,
		FromAddress: ccpeer.ReplyAddress(m.inboxPath),
		FromName:    m.sessionName,
		FromMode:    "prompting",
	})
	if err != nil {
		m.t.Fatalf("%s: NewFrame: %v", m.person, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := ccpeer.NewClient(m.platform, m.registry, quiet())
	if err := client.Deliver(ctx, target, frame); err != nil {
		m.t.Fatalf("%s: Deliver: %v", m.person, err)
	}
}

// awaitMessage waits for something to arrive in this machine's session inbox.
func (m *machine) awaitMessage(within time.Duration) ccpeer.Envelope {
	m.t.Helper()

	select {
	case f := <-m.inbox.Frames():
		env, err := ccpeer.ParseEnvelope(f.Message.Content)
		if err != nil {
			m.t.Fatalf("%s: ParseEnvelope: %v", m.person, err)
		}
		return env
	case <-time.After(within):
		m.t.Fatalf("%s: nothing arrived in the session inbox", m.person)
		return ccpeer.Envelope{}
	}
}

// TestTwoPeopleTwoConnectorsOneMessage is the definition of done for version one.
//
// Two people, two separate connectors with their own identities and their own
// session registries, a real relay between them, and a message that leaves one
// person's session and arrives in the other person's session inbox, sealed on the
// way and framed on arrival.
//
// The only thing this shares with a real deployment is the computer it runs on.
func TestTwoPeopleTwoConnectorsOneMessage(t *testing.T) {
	store, err := workspace.OpenStore(filepath.Join(t.TempDir(), "workspace.json"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	alice, _ := identity.Generate()
	bob, _ := identity.Generate()

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

	server := httptest.NewServer(relay.NewServer(store, quiet()).Handler())
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/connect"

	aliceMachine := newMachine(t, "alice", "alice-cc", url, alice)
	bobMachine := newMachine(t, "bob", "bob-cc", url, bob)

	// Alice's connector publishes a native peer for Bob's session.
	peer := aliceMachine.waitForPeer("bob-bob-cc", 60*time.Second)

	const text = "el schema cambio, tenant_id es la columna nueva"
	aliceMachine.write(peer, text)

	got := bobMachine.awaitMessage(60 * time.Second)

	if !strings.Contains(got.Text, text) {
		t.Errorf("the message that arrived does not contain what was sent:\n%s", got.Text)
	}
	if got.FromName != "alice-alice-cc" {
		t.Errorf("FromName = %q, want the peer standing for alice's session", got.FromName)
	}
	if got.From == "" {
		t.Error("the message arrived with no reply address, so bob cannot answer")
	}

	// The default level is collaborator, so the text has to be framed as coming
	// from another person rather than passed through as though it were one of
	// bob's own sessions.
	if !strings.Contains(got.Text, "another person") {
		t.Errorf("the message was not framed as coming from another person:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "alice") {
		t.Errorf("the framing does not name the sender:\n%s", got.Text)
	}
}

// TestTrustPeerPassesTheTextThrough shows the other end of the same path. A
// person granted peer gets their text delivered as written.
func TestTrustPeerPassesTheTextThrough(t *testing.T) {
	t.Parallel()

	const text = "rebasar sobre main ya es seguro"
	framed := trust.Frame(trust.Peer, trust.Sender{
		Person: "alice", Session: "alice-cc", Workspace: "acme", Mode: "prompting",
	}, text)

	if framed != text {
		t.Errorf("at peer the text should be passed through, got:\n%s", framed)
	}
}
