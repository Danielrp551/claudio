package ccpeer

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// TestMain fails the package if any test leaves a goroutine behind. Every
// goroutine this package creates belongs to an Inbox, and an Inbox that does not
// stop cleanly would leave a peer visible to Claude Code with nobody answering.
//
// One goroutine is ignored on purpose. go-winio starts a single completion port
// processor the first time it touches a pipe, and that goroutine lives for the
// life of the process by design. It belongs to the library, not to us, and it is
// named precisely so the ignore cannot hide a leak of our own.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("github.com/Microsoft/go-winio.ioCompletionProcessor"),
	)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestInboxRoundTrip binds a real endpoint on this platform, publishes a real
// record for this process, and delivers a frame to itself through the real
// client, including the authentication line where the platform requires one.
//
// It is the closest thing to the live protocol that can run without Claude Code
// installed, and it covers the two halves that a unit test cannot: that the
// platform can bind and dial its own endpoint, and that the authentication line
// the client sends is the one the inbox expects.
func TestInboxRoundTrip(t *testing.T) {
	platform := Platform()
	if !platform.Verified() {
		t.Skipf("%s is not a verified platform, publishing a record is refused on purpose", platform.GOOS())
	}

	reg, err := NewRegistry(platform, t.TempDir())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	path, err := platform.NewInboxPath()
	if err != nil {
		t.Fatalf("NewInboxPath: %v", err)
	}

	pub, err := reg.Publish("test-peer", path)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	t.Cleanup(func() {
		if err := reg.Withdraw(pub); err != nil {
			t.Errorf("Withdraw: %v", err)
		}
	})

	in, err := Listen(platform, path, pub.Token, quietLogger())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() {
		if err := in.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	frame, err := NewFrame(FrameOptions{
		Text:        "una prueba de ida y vuelta",
		FromAddress: ReplyAddress(path),
		FromName:    "test-sender",
		FromMode:    "prompting",
	})
	if err != nil {
		t.Fatalf("NewFrame: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := NewClient(platform, reg, quietLogger())
	if err := client.Deliver(ctx, pub.Record, frame); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	select {
	case got := <-in.Frames():
		if got.MsgID != frame.MsgID {
			t.Errorf("MsgID = %q, want %q", got.MsgID, frame.MsgID)
		}
		env, err := ParseEnvelope(got.Message.Content)
		if err != nil {
			t.Fatalf("ParseEnvelope: %v", err)
		}
		if env.FromName != "test-sender" {
			t.Errorf("FromName = %q, want %q", env.FromName, "test-sender")
		}
		if env.Text != "una prueba de ida y vuelta" {
			t.Errorf("Text = %q, want the sent text", env.Text)
		}
	case <-ctx.Done():
		t.Fatal("no frame arrived before the deadline")
	}
}

// TestInboxRefusesABadAuthLine checks the half of the security model that is
// easy to forget: a connection that authenticates with the wrong token must not
// have its frames delivered.
func TestInboxRefusesABadAuthLine(t *testing.T) {
	platform := Platform()

	path, err := platform.NewInboxPath()
	if err != nil {
		t.Fatalf("NewInboxPath: %v", err)
	}

	in, err := Listen(platform, path, "the-real-token", quietLogger())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer in.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := platform.Dial(ctx, path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_, err = conn.Write([]byte(`{"type":"auth","token":"the-wrong-token"}` + "\n" +
		`{"msgV":1,"msg_id":"x","type":"user","message":{"role":"user","content":"x"}}` + "\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	conn.Close()

	select {
	case f := <-in.Frames():
		t.Fatalf("a frame was delivered despite a bad authentication line: %+v", f)
	case <-time.After(300 * time.Millisecond):
		// Nothing arrived, which is the expected outcome.
	}
}

// TestInboxCloseIsIdempotent matters because Close runs on several shutdown
// paths and a second call must not panic or block.
func TestInboxCloseIsIdempotent(t *testing.T) {
	platform := Platform()

	path, err := platform.NewInboxPath()
	if err != nil {
		t.Fatalf("NewInboxPath: %v", err)
	}
	in, err := Listen(platform, path, "", quietLogger())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	if err := in.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := in.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if _, open := <-in.Frames(); open {
		t.Error("the frame channel should be closed after Close")
	}
}
