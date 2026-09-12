package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/danielrp551/claudio/internal/ccpeer"
)

// claudioBinary is built once for the whole package. The supervisor spawns real
// child processes, so testing it against a stub would test the stub. Building
// the real binary costs a few seconds and buys a test that exercises the actual
// control protocol between a daemon and a ghost.
var claudioBinary string

func TestMain(m *testing.M) {
	code, err := runTests(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon tests:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runTests(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "claudio-daemon-test-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)

	claudioBinary = filepath.Join(dir, "claudio")
	if runtime.GOOS == "windows" {
		claudioBinary += ".exe"
	}

	build := exec.CommandContext(context.Background(), "go", "build", "-o", claudioBinary, "../../cmd/claudio")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return 0, fmt.Errorf("building the test binary: %w", err)
	}

	// goleak runs after the tests, and the deferred cleanup above runs after
	// that, which is the order we want.
	code := m.Run()
	if code == 0 {
		if err := goleak.Find(
			goleak.IgnoreAnyFunction("github.com/Microsoft/go-winio.ioCompletionProcessor"),
		); err != nil {
			fmt.Fprintln(os.Stderr, "goroutine leak:", err)
			return 1, nil
		}
	}
	return code, nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fixture struct {
	t        *testing.T
	sessions string
	index    *orphanIndex
	sup      *supervisor
	platform ccpeer.LocalEndpoint
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	platform := ccpeer.Platform()
	if !platform.Verified() {
		t.Skipf("%s is not a verified platform, a ghost refuses to publish there", platform.GOOS())
	}

	sessions := t.TempDir()
	index, err := newOrphanIndex(filepath.Join(t.TempDir(), "ghosts.json"))
	if err != nil {
		t.Fatalf("newOrphanIndex: %v", err)
	}

	sup := newSupervisor(context.Background(), claudioBinary, []string{"ghost"},
		sessions, platform, index, quietLogger())

	t.Cleanup(func() {
		if err := sup.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	return &fixture{t: t, sessions: sessions, index: index, sup: sup, platform: platform}
}

// waitForPeer waits until a peer reports ready, which is the point at which its
// record is on disk and its endpoint is bound.
func (f *fixture) waitForPeer(name string) PeerStatus {
	f.t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range f.sup.Peers() {
			if p.Name == name && p.Ready {
				return p
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("the peer %q never became ready, peers are %+v", name, f.sup.Peers())
	return PeerStatus{}
}

func (f *fixture) records() []string {
	f.t.Helper()
	matches, err := filepath.Glob(filepath.Join(f.sessions, "*.json"))
	if err != nil {
		f.t.Fatalf("globbing records: %v", err)
	}
	return matches
}

func TestSupervisorStartsAndStopsAPeer(t *testing.T) {
	f := newFixture(t)

	if err := f.sup.Ensure("luis-api"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	peer := f.waitForPeer("luis-api")

	if peer.PID == 0 {
		t.Error("a ready peer must report a process id")
	}
	if peer.Endpoint == "" {
		t.Error("a ready peer must report an endpoint")
	}
	if got := f.records(); len(got) != 1 {
		t.Fatalf("there are %d records, want 1: %v", len(got), got)
	}

	f.sup.Release("luis-api")

	if got := f.records(); len(got) != 0 {
		t.Errorf("records left behind after Release: %v", got)
	}
	if len(f.sup.Peers()) != 0 {
		t.Errorf("the peer is still listed after Release: %+v", f.sup.Peers())
	}
}

// TestSupervisorEnsureIsIdempotent matters because the discovery loop calls it
// on every poll for every peer it wants.
func TestSupervisorEnsureIsIdempotent(t *testing.T) {
	f := newFixture(t)

	for range 5 {
		if err := f.sup.Ensure("same-peer"); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
	}
	f.waitForPeer("same-peer")

	if n := len(f.sup.Peers()); n != 1 {
		t.Fatalf("there are %d peers, want 1", n)
	}
	if got := f.records(); len(got) != 1 {
		t.Fatalf("there are %d records, want 1: %v", len(got), got)
	}
}

func TestSupervisorRunsSeveralPeers(t *testing.T) {
	f := newFixture(t)

	names := []string{"luis-api", "luis-front", "ws-acme"}
	for _, n := range names {
		if err := f.sup.Ensure(n); err != nil {
			t.Fatalf("Ensure %s: %v", n, err)
		}
	}
	for _, n := range names {
		f.waitForPeer(n)
	}

	if got := f.records(); len(got) != len(names) {
		t.Fatalf("there are %d records, want %d: %v", len(got), len(names), got)
	}

	if err := f.sup.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := f.records(); len(got) != 0 {
		t.Errorf("records left behind after Close: %v", got)
	}
}

// TestSupervisorRestartsAKilledPeer covers the case a user will actually hit:
// something kills a ghost, and the peer has to come back without help.
func TestSupervisorRestartsAKilledPeer(t *testing.T) {
	f := newFixture(t)

	if err := f.sup.Ensure("resilient"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	first := f.waitForPeer("resilient")

	proc, err := os.FindProcess(first.PID)
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range f.sup.Peers() {
			if p.Name == "resilient" && p.Ready && p.PID != first.PID {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the peer did not come back after being killed, peers are %+v", f.sup.Peers())
}

// TestSupervisorForwardsFrames delivers to the endpoint a peer published and
// checks the frame reaches the supervisor's channel tagged with the right peer.
func TestSupervisorForwardsFrames(t *testing.T) {
	f := newFixture(t)

	if err := f.sup.Ensure("receiver"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	peer := f.waitForPeer("receiver")

	reg, err := ccpeer.NewRegistry(f.platform, f.sessions)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	target, err := reg.Get(peer.PID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	frame, err := ccpeer.NewFrame(ccpeer.FrameOptions{
		Text:        "el deploy quedo",
		FromAddress: ccpeer.ReplyAddress("uds:/tmp/whoever.sock"),
		FromName:    "whoever",
	})
	if err != nil {
		t.Fatalf("NewFrame: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := ccpeer.NewClient(f.platform, reg, quietLogger()).Deliver(ctx, target, frame); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	select {
	case in := <-f.sup.Frames():
		if in.Peer != "receiver" {
			t.Errorf("Peer = %q, want %q", in.Peer, "receiver")
		}
		if in.Frame.MsgID != frame.MsgID {
			t.Errorf("MsgID = %q, want %q", in.Frame.MsgID, frame.MsgID)
		}
	case <-ctx.Done():
		t.Fatal("the frame never reached the supervisor")
	}
}

// TestOrphanIndexSweepsOnlyDeadProcesses is the guard against the worst thing
// this package could do: remove the record of a live Claude Code session.
func TestOrphanIndexSweepsOnlyDeadProcesses(t *testing.T) {
	t.Parallel()

	sessions := t.TempDir()
	index, err := newOrphanIndex(filepath.Join(t.TempDir(), "ghosts.json"))
	if err != nil {
		t.Fatalf("newOrphanIndex: %v", err)
	}

	// This process is alive, so its record must survive a sweep even though it
	// is tracked.
	alive := os.Getpid()
	writeStubRecord(t, sessions, alive)
	if err := index.Track(alive, "alive", sessions); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// A process id that cannot be running. Its record is ours to remove.
	const dead = 999999
	writeStubRecord(t, sessions, dead)
	if err := index.Track(dead, "dead", sessions); err != nil {
		t.Fatalf("Track: %v", err)
	}

	removed, err := index.Sweep(ccpeer.Platform())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed != 1 {
		t.Errorf("Sweep removed %d records, want 1", removed)
	}

	if _, err := os.Stat(filepath.Join(sessions, fmt.Sprintf("%d.json", alive))); err != nil {
		t.Errorf("the record of a live process was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessions, fmt.Sprintf("%d.json", dead))); !os.IsNotExist(err) {
		t.Errorf("the record of a dead process survived: %v", err)
	}
}

func TestOrphanIndexSurvivesARestart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ghosts.json")
	sessions := t.TempDir()

	first, err := newOrphanIndex(path)
	if err != nil {
		t.Fatalf("newOrphanIndex: %v", err)
	}
	if err := first.Track(4242, "peer", sessions); err != nil {
		t.Fatalf("Track: %v", err)
	}

	second, err := newOrphanIndex(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	tracked := second.Tracked()
	if len(tracked) != 1 || tracked[0].PID != 4242 {
		t.Fatalf("the reopened index holds %+v, want the one entry", tracked)
	}
}

func writeStubRecord(t *testing.T, dir string, pid int) {
	t.Helper()
	body := fmt.Sprintf(`{"pid":%d,"peerProtocol":1,"messagingSocketPath":"/tmp/x","name":"stub"}`, pid)
	path := filepath.Join(dir, fmt.Sprintf("%d.json", pid))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
