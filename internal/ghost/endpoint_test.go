//go:build linux || darwin

package ghost

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Danielrp551/claudio/internal/ccpeer"
)

// TestAGhostGivesUpWhenItsEndpointIsRemoved is the regression test for a failure
// that produced no error anywhere.
//
// On the Unix platforms the socket a peer is bound to lives in a directory that
// belongs to a login session, and systemd removes it when the last one ends.
// That happens to anybody who starts the connector over ssh and then logs out.
// The listener goes on listening, the record goes on saying the peer is ready,
// and no session can reach it ever again.
//
// A ghost cannot rebind without tearing down the channel its frames arrive on,
// so the contract is that it says what happened and stops, and the supervisor
// replaces it. This test removes the socket underneath a running ghost and
// checks both halves of that: the reason is reported, and the process ends.
func TestAGhostGivesUpWhenItsEndpointIsRemoved(t *testing.T) {
	platform := ccpeer.Platform()
	if !platform.Verified() {
		t.Skipf("%s is not a verified platform", platform.GOOS())
	}

	sessions := t.TempDir()

	// The parent side of the control protocol. Standard input stays open, so
	// nothing ends the ghost except the failure under test.
	commands, toGhost := io.Pipe()
	fromGhost, events := io.Pipe()
	t.Cleanup(func() {
		_ = toGhost.Close()
		_ = fromGhost.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			In:        commands,
			Out:       events,
			Platform:  platform,
			Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
			Heartbeat: 100 * time.Millisecond,
		})
	}()

	enc := json.NewEncoder(toGhost)
	if err := enc.Encode(Command{
		Op: OpInit, Name: "test-peer", SessionsDir: sessions,
	}); err != nil {
		t.Fatalf("sending init: %v", err)
	}

	dec := json.NewDecoder(fromGhost)
	var ready Event
	if err := dec.Decode(&ready); err != nil {
		t.Fatalf("waiting for ready: %v", err)
	}
	if ready.Op != OpReady {
		t.Fatalf("the first event is %q, want %q", ready.Op, OpReady)
	}
	if ready.Endpoint == "" {
		t.Fatal("the ready event carries no endpoint")
	}

	// This is what the operating system does behind the tool's back.
	if err := os.Remove(ready.Endpoint); err != nil {
		t.Fatalf("removing the endpoint: %v", err)
	}

	// The ghost has to notice and say so rather than carry on unreachable.
	deadline := time.After(30 * time.Second)
	reported := make(chan string, 1)
	go func() {
		for {
			var ev Event
			if err := dec.Decode(&ev); err != nil {
				return
			}
			if ev.Op == OpError {
				reported <- ev.Message
				return
			}
		}
	}()

	select {
	case msg := <-reported:
		if !strings.Contains(msg, ready.Endpoint) {
			t.Errorf("the reported reason does not name the endpoint: %q", msg)
		}
		if !strings.Contains(msg, "gone") {
			t.Errorf("the reported reason does not say the endpoint went away: %q", msg)
		}
	case <-deadline:
		t.Fatal("the ghost never noticed that its endpoint had been removed")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Error("the ghost reported the problem and then kept running anyway")
	}
}
