package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Danielrp551/claudio/internal/ccpeer"
)

// TestDaemonClosesTheLoopLocally is the milestone this project is built around,
// written as a test.
//
// The test plays the part of a Claude Code session. It publishes a record and
// binds an endpoint the way a real session does, so the daemon sees it as a local
// session. It then writes a frame to the native peer the daemon published for a
// remote session, exactly as a real Claude would when its user says "tell
// luis-api the schema changed".
//
// The daemon has to route that frame to the transport, take the answer, and
// deliver it back into the test's inbox with a reply address that points at
// something this machine actually listens on.
//
// Everything in that path is real except the network.
func TestDaemonClosesTheLoopLocally(t *testing.T) {
	platform := ccpeer.Platform()
	if !platform.Verified() {
		t.Skipf("%s is not a verified platform", platform.GOOS())
	}

	sessions := t.TempDir()
	state := filepath.Join(t.TempDir(), "ghosts.json")

	remote := RemoteSession{
		ID: "s-1", Person: "luis", Machine: "thinkpad", Session: "api", Status: "idle",
	}

	transport := NewLoopback([]RemoteSession{remote}, func(msg Outbound) string {
		return "recibido: " + msg.Text
	})

	d, err := New(Options{
		Platform:     platform,
		SessionsDirs: []string{sessions},
		StatePath:    state,
		Executable:   claudioBinary,
		GhostArgs:    []string{"ghost"},
		Workspace:    "acme",
		Transport:    transport,
		Logger:       quietLogger(),
		PollInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("the daemon did not stop")
		}
	})

	// Play the part of a local Claude Code session: publish a record and listen
	// on the endpoint it names.
	sessionReg, err := ccpeer.NewRegistry(platform, sessions)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	inboxPath, err := platform.NewInboxPath()
	if err != nil {
		t.Fatalf("NewInboxPath: %v", err)
	}
	inbox, err := ccpeer.Listen(platform, inboxPath, "", quietLogger())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer inbox.Close()

	pub, err := sessionReg.Publish("herramientas-22", inboxPath)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	defer sessionReg.Withdraw(pub)
	inbox.SetToken(pub.Token)

	// Wait for the daemon to publish a native peer for the remote session and to
	// notice the local session.
	peerName := PeerName("acme", remote, false)
	peer := waitForRecord(t, sessionReg, peerName, 30*time.Second)

	// Write to that peer the way Claude Code would.
	frame, err := ccpeer.NewFrame(ccpeer.FrameOptions{
		Text:        "el schema cambio, tenant_id es la columna nueva",
		FromAddress: ccpeer.ReplyAddress(inboxPath),
		FromName:    "herramientas-22",
		FromMode:    "prompting",
	})
	if err != nil {
		t.Fatalf("NewFrame: %v", err)
	}

	sendCtx, sendCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer sendCancel()

	client := ccpeer.NewClient(platform, sessionReg, quietLogger())
	if err := client.Deliver(sendCtx, peer, frame); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	// The answer has to come back into this inbox.
	select {
	case got := <-inbox.Frames():
		env, err := ccpeer.ParseEnvelope(got.Message.Content)
		if err != nil {
			t.Fatalf("ParseEnvelope: %v", err)
		}
		if !strings.Contains(env.Text, "el schema cambio") {
			t.Errorf("the answer does not carry the original message: %q", env.Text)
		}
		if env.FromName != peerName {
			t.Errorf("FromName = %q, want %q", env.FromName, peerName)
		}
		if env.From == "" {
			t.Error("the answer carries no reply address")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no answer came back into the session inbox")
	}
}

// TestDaemonEvictsBeyondTheCap checks the promise that the number of processes
// belongs to the user: over the cap, extra sessions do not get a peer, and
// nothing becomes unreachable because the workspace peer remains.
func TestDaemonEvictsBeyondTheCap(t *testing.T) {
	platform := ccpeer.Platform()
	if !platform.Verified() {
		t.Skipf("%s is not a verified platform", platform.GOOS())
	}

	sessions := t.TempDir()
	roster := []RemoteSession{
		{ID: "a", Person: "luis", Session: "api"},
		{ID: "b", Person: "luis", Session: "front"},
		{ID: "c", Person: "ana", Session: "infra"},
	}

	d, err := New(Options{
		Platform:     platform,
		SessionsDirs: []string{sessions},
		StatePath:    filepath.Join(t.TempDir(), "ghosts.json"),
		Executable:   claudioBinary,
		Workspace:    "acme",
		Policy:       Policy{Mode: ModeAuto, MaxGhosts: 2},
		Transport:    NewLoopback(roster, nil),
		Logger:       quietLogger(),
		PollInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	reg, err := ccpeer.NewRegistry(platform, sessions)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Two session peers plus the workspace peer.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		records, err := reg.List()
		if err == nil && len(records) == 3 {
			names := map[string]bool{}
			for _, r := range records {
				names[r.Name] = true
			}
			if !names[WorkspacePeerName("acme")] {
				t.Fatalf("the workspace peer is missing, names are %v", names)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	records, _ := reg.List()
	t.Fatalf("there are %d peers, want 3 with a cap of 2 plus the workspace peer", len(records))
}

func TestSplitMention(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		in          string
		wantMention string
		wantRest    string
	}{
		{name: "a mention with a colon", in: "@luis/api: revisa el schema", wantMention: "luis/api", wantRest: "revisa el schema"},
		{name: "a mention with a space", in: "@luis/api revisa el schema", wantMention: "luis/api", wantRest: "revisa el schema"},
		{name: "a mention on its own line", in: "@luis/api\nrevisa el schema", wantMention: "luis/api", wantRest: "revisa el schema"},
		{name: "leading space before the mention", in: "  @ana-infra: hola", wantMention: "ana-infra", wantRest: "hola"},
		{name: "no mention at all", in: "revisa el schema", wantMention: "", wantRest: "revisa el schema"},
		{name: "a mention with nothing after it", in: "@luis/api", wantMention: "luis/api", wantRest: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mention, rest := splitMention(tt.in)
			if mention != tt.wantMention {
				t.Errorf("mention = %q, want %q", mention, tt.wantMention)
			}
			if rest != tt.wantRest {
				t.Errorf("rest = %q, want %q", rest, tt.wantRest)
			}
		})
	}
}

func TestPeerNameIsSafeToMention(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		workspace string
		session   RemoteSession
		withWS    bool
		want      string
	}{
		{
			name:    "person and session",
			session: RemoteSession{Person: "luis", Session: "api"},
			want:    "luis-api",
		},
		{
			name:      "with the workspace in front",
			workspace: "acme",
			session:   RemoteSession{Person: "luis", Session: "api"},
			withWS:    true,
			want:      "acme-luis-api",
		},
		{
			name:    "characters the mention typeahead would need quoting for",
			session: RemoteSession{Person: "luis@thinkpad", Session: "proyectos/api"},
			want:    "luis-thinkpad-proyectos-api",
		},
		{
			name:    "falls back to the identifier",
			session: RemoteSession{ID: "s-42"},
			want:    "s-42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := PeerName(tt.workspace, tt.session, tt.withWS)
			if got != tt.want {
				t.Errorf("PeerName = %q, want %q", got, tt.want)
			}
			if unsafeForMention.MatchString(got) {
				t.Errorf("PeerName produced %q, which the mention typeahead would need quoting for", got)
			}
		})
	}
}

func waitForRecord(t *testing.T, reg *ccpeer.Registry, name string, within time.Duration) ccpeer.Record {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		rec, err := reg.Find(name)
		if err == nil {
			return rec
		}
		time.Sleep(50 * time.Millisecond)
	}
	records, _ := reg.List()
	names := make([]string, 0, len(records))
	for _, r := range records {
		names = append(names, r.Name)
	}
	t.Fatalf("the peer %q never appeared, the records are %v", name, names)
	return ccpeer.Record{}
}
