package ghost

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/Danielrp551/claudio/internal/ccpeer"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("github.com/Microsoft/go-winio.ioCompletionProcessor"),
	)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// harness runs one ghost against a temporary session directory and gives the
// test the two ends of its control protocol.
type harness struct {
	t          *testing.T
	sessions   string
	toGhost    *io.PipeWriter
	events     *bufio.Scanner
	done       chan error
	recordPath string
}

func start(t *testing.T, heartbeat time.Duration) *harness {
	t.Helper()

	platform := ccpeer.Platform()
	if !platform.Verified() {
		t.Skipf("%s is not a verified platform, a ghost refuses to publish there", platform.GOOS())
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	h := &harness{
		t:        t,
		sessions: t.TempDir(),
		toGhost:  inW,
		events:   bufio.NewScanner(outR),
		done:     make(chan error, 1),
	}

	go func() {
		err := Run(context.Background(), Config{
			In:        inR,
			Out:       outW,
			Platform:  platform,
			Logger:    quietLogger(),
			Heartbeat: heartbeat,
		})
		outW.Close()
		h.done <- err
	}()

	h.send(Command{Op: OpInit, Name: "test-ghost", SessionsDir: h.sessions})

	ready := h.next()
	if ready.Op != OpReady {
		t.Fatalf("first event is %q, want %q", ready.Op, OpReady)
	}
	if ready.PID == 0 || ready.Endpoint == "" || ready.RecordPath == "" {
		t.Fatalf("ready event is incomplete: %+v", ready)
	}
	h.recordPath = ready.RecordPath

	return h
}

func (h *harness) send(c Command) {
	h.t.Helper()
	blob, err := json.Marshal(c)
	if err != nil {
		h.t.Fatalf("marshalling a command: %v", err)
	}
	if _, err := h.toGhost.Write(append(blob, '\n')); err != nil {
		h.t.Fatalf("writing a command: %v", err)
	}
}

func (h *harness) next() Event {
	h.t.Helper()
	if !h.events.Scan() {
		h.t.Fatalf("no event arrived: %v", h.events.Err())
	}
	var e Event
	if err := json.Unmarshal(h.events.Bytes(), &e); err != nil {
		h.t.Fatalf("unmarshalling an event: %v", err)
	}
	return e
}

// stopByClosingStdin simulates the parent dying, which is the path that has to
// work even when nothing polite happens.
func (h *harness) stopByClosingStdin() {
	h.t.Helper()
	h.toGhost.Close()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Fatalf("Run returned %v, want nil for an orderly stop", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Fatal("the ghost did not stop after its control stream closed")
	}
}

func TestGhostPublishesAndWithdraws(t *testing.T) {
	h := start(t, DefaultHeartbeat)

	if _, err := os.Stat(h.recordPath); err != nil {
		t.Fatalf("the record should exist while the ghost runs: %v", err)
	}

	h.stopByClosingStdin()

	if _, err := os.Stat(h.recordPath); !os.IsNotExist(err) {
		t.Errorf("the record still exists after the ghost stopped: %v", err)
	}
	if left := remaining(t, h.sessions); len(left) != 0 {
		t.Errorf("the session directory is not empty after the ghost stopped: %v", left)
	}
}

// TestGhostHeartbeatDoesNotResurrectTheRecord is the regression test for a bug
// that actually happened during research.
//
// A probe removed its record on the way out while its heartbeat goroutine was
// still running. The heartbeat then rewrote the file, and an orphan record was
// left in the user's session directory.
//
// The heartbeat here is deliberately fast and the scenario runs many times,
// because the failure is a race and a single pass proves very little.
func TestGhostHeartbeatDoesNotResurrectTheRecord(t *testing.T) {
	const rounds = 25

	for i := range rounds {
		t.Run("round "+strconv.Itoa(i), func(t *testing.T) {
			h := start(t, time.Millisecond)

			// Let several heartbeats land, so the goroutine is certainly in the
			// middle of its loop when the stop arrives.
			time.Sleep(15 * time.Millisecond)

			h.stopByClosingStdin()

			if left := remaining(t, h.sessions); len(left) != 0 {
				t.Fatalf("the heartbeat left files behind: %v", left)
			}
		})
	}
}

func TestGhostStopsOnShutdownCommand(t *testing.T) {
	h := start(t, DefaultHeartbeat)

	h.send(Command{Op: OpShutdown})

	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the ghost ignored the shutdown command")
	}

	if left := remaining(t, h.sessions); len(left) != 0 {
		t.Errorf("files left behind after shutdown: %v", left)
	}
	h.toGhost.Close()
}

// TestGhostForwardsFrames delivers a real frame to the endpoint the ghost
// published and checks that it comes out of the control protocol unchanged.
func TestGhostForwardsFrames(t *testing.T) {
	platform := ccpeer.Platform()
	h := start(t, DefaultHeartbeat)

	reg, err := ccpeer.NewRegistry(platform, h.sessions)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	target, err := reg.Get(readPID(t, h.recordPath))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	frame, err := ccpeer.NewFrame(ccpeer.FrameOptions{
		Text:        "hola desde el otro lado",
		FromAddress: ccpeer.ReplyAddress("uds:/tmp/sender.sock"),
		FromName:    "sender",
		FromMode:    "prompting",
	})
	if err != nil {
		t.Fatalf("NewFrame: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := ccpeer.NewClient(platform, reg, quietLogger()).Deliver(ctx, target, frame); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	got := h.next()
	if got.Op != OpFrame {
		t.Fatalf("event is %q, want %q", got.Op, OpFrame)
	}
	if got.Frame == nil || got.Frame.MsgID != frame.MsgID {
		t.Fatalf("the forwarded frame does not match what was sent: %+v", got.Frame)
	}

	h.stopByClosingStdin()
}

func TestGhostRefusesAControlStreamWithoutInit(t *testing.T) {
	inR, inW := io.Pipe()
	var out discardWriter

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Config{
			In: inR, Out: &out, Logger: quietLogger(),
		})
	}()

	// Closing without ever sending init is what a broken parent looks like.
	inW.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil, want an error when init never arrived")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func remaining(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func readPID(t *testing.T, recordPath string) int {
	t.Helper()
	base := filepath.Base(recordPath)
	pid, err := strconv.Atoi(base[:len(base)-len(filepath.Ext(base))])
	if err != nil {
		t.Fatalf("parsing the process id out of %q: %v", base, err)
	}
	return pid
}
