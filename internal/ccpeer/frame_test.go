package ccpeer

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestDecodeFrameGolden reads a frame captured from a live Claude Code 2.1.269
// session on Windows. It is the regression test that matters most: if a future
// release changes the wire format, this fails before anything else does.
func TestDecodeFrameGolden(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("testdata/frame-v1-windows.json")
	if err != nil {
		t.Fatalf("reading the golden frame: %v", err)
	}

	f, err := DecodeFrame([]byte(strings.TrimSpace(string(raw))))
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}

	const wantPipe = `uds:\\.\pipe\LOCAL\cc-msg-9ea9dcfbd69ef3a2b7eaebac65b2b11f`

	if f.MsgV != FrameVersion {
		t.Errorf("MsgV = %d, want %d", f.MsgV, FrameVersion)
	}
	if f.Type != "user" {
		t.Errorf("Type = %q, want %q", f.Type, "user")
	}
	if f.Priority != PriorityNext {
		t.Errorf("Priority = %q, want %q", f.Priority, PriorityNext)
	}
	if f.From != wantPipe {
		t.Errorf("From = %q, want %q", f.From, wantPipe)
	}

	env, err := ParseEnvelope(f.Message.Content)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.FromName != "herramientas-22" {
		t.Errorf("FromName = %q, want %q", env.FromName, "herramientas-22")
	}
	if env.FromMode != "prompting" {
		t.Errorf("FromMode = %q, want %q", env.FromMode, "prompting")
	}
	if env.From != wantPipe {
		t.Errorf("envelope From = %q, want %q", env.From, wantPipe)
	}
	if !strings.HasPrefix(env.Text, "Prueba de entrega") {
		t.Errorf("Text = %q, want it to start with the captured sentence", env.Text)
	}
}

func TestDecodeFrame(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		line    string
		wantErr bool
	}{
		{
			name: "current version is accepted",
			line: `{"msgV":1,"msg_id":"a","type":"user","message":{"role":"user","content":"x"}}`,
		},
		{
			name:    "a newer version is refused rather than guessed at",
			line:    `{"msgV":2,"msg_id":"a","type":"user","message":{"role":"user","content":"x"}}`,
			wantErr: true,
		},
		{
			name:    "a missing version is refused",
			line:    `{"msg_id":"a","type":"user","message":{"role":"user","content":"x"}}`,
			wantErr: true,
		},
		{
			name:    "malformed json is refused",
			line:    `{"msgV":1,`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := DecodeFrame([]byte(tt.line))
			if tt.wantErr && err == nil {
				t.Fatal("want an error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("want no error, got %v", err)
			}
		})
	}
}

func TestParseEnvelope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  string
		wantErr  bool
		wantName string
		wantText string
	}{
		{
			name:     "a well formed element",
			content:  "<cross-session-message from=\"uds:/tmp/a.sock\" from-name=\"ana\" from-mode=\"prompting\">\nhola\n</cross-session-message>",
			wantName: "ana",
			wantText: "hola",
		},
		{
			name:     "several lines of text survive intact",
			content:  "<cross-session-message from=\"uds:/tmp/a.sock\" from-name=\"ana\" from-mode=\"prompting\">\nuna\ndos\n</cross-session-message>",
			wantName: "ana",
			wantText: "una\ndos",
		},
		{
			name:     "escaped characters in an attribute are restored",
			content:  "<cross-session-message from=\"uds:/tmp/a.sock\" from-name=\"a&quot;b\" from-mode=\"prompting\">\nx\n</cross-session-message>",
			wantName: `a"b`,
			wantText: "x",
		},
		{
			name:    "bare text is refused because it carries no attribution",
			content: "just some text",
			wantErr: true,
		},
		{
			name:    "a missing sender name is refused",
			content: "<cross-session-message from=\"uds:/tmp/a.sock\" from-mode=\"prompting\">\nx\n</cross-session-message>",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env, err := ParseEnvelope(tt.content)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("want no error, got %v", err)
			}
			if env.FromName != tt.wantName {
				t.Errorf("FromName = %q, want %q", env.FromName, tt.wantName)
			}
			if env.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", env.Text, tt.wantText)
			}
		})
	}
}

// TestNewFrameRoundTrip is the guard against the class of bug that would break
// Windows first: an address full of backslashes must survive being wrapped,
// serialized, parsed, and unwrapped, byte for byte.
func TestNewFrameRoundTrip(t *testing.T) {
	t.Parallel()

	addresses := []string{
		`uds:\\.\pipe\LOCAL\cc-msg-9ea9dcfbd69ef3a2b7eaebac65b2b11f`,
		`uds:\\.\pipe\cc-msg-0a900ddc58602198fdb5b83cb3a8b6a2`,
		"uds:/run/user/1000/cc-socks/803.sock",
	}

	for _, addr := range addresses {
		t.Run(addr, func(t *testing.T) {
			t.Parallel()

			f, err := NewFrame(FrameOptions{
				Text:        "el schema cambio",
				FromAddress: addr,
				FromName:    "luis-api",
				FromMode:    "prompting",
			})
			if err != nil {
				t.Fatalf("NewFrame: %v", err)
			}

			blob, err := json.Marshal(f)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			back, err := DecodeFrame(blob)
			if err != nil {
				t.Fatalf("DecodeFrame: %v", err)
			}
			if back.From != addr {
				t.Errorf("From = %q, want %q", back.From, addr)
			}

			env, err := ParseEnvelope(back.Message.Content)
			if err != nil {
				t.Fatalf("ParseEnvelope: %v", err)
			}
			if env.From != addr {
				t.Errorf("envelope From = %q, want %q", env.From, addr)
			}
			if env.FromName != "luis-api" {
				t.Errorf("FromName = %q, want %q", env.FromName, "luis-api")
			}
			if env.Text != "el schema cambio" {
				t.Errorf("Text = %q, want %q", env.Text, "el schema cambio")
			}
		})
	}
}

func TestNewFrameRejectsIncompleteOptions(t *testing.T) {
	t.Parallel()

	if _, err := NewFrame(FrameOptions{Text: "x", FromName: "a"}); err == nil {
		t.Error("want an error when the reply address is missing")
	}
	if _, err := NewFrame(FrameOptions{Text: "x", FromAddress: "uds:/tmp/a"}); err == nil {
		t.Error("want an error when the sender name is missing")
	}
}

// FuzzParseEnvelope exercises the only function in this package that reads text
// written by another person. Anything it does other than returning a value or an
// error is a finding.
func FuzzParseEnvelope(f *testing.F) {
	f.Add("<cross-session-message from=\"uds:/tmp/a\" from-name=\"ana\" from-mode=\"prompting\">\nhola\n</cross-session-message>")
	f.Add("<cross-session-message>")
	f.Add("")
	f.Add("<cross-session-message from-name=\"\">x</cross-session-message>")

	f.Fuzz(func(t *testing.T, content string) {
		env, err := ParseEnvelope(content)
		if err != nil {
			return
		}
		// A successful parse must always carry attribution, because delivering
		// unattributed content is the thing this function exists to prevent.
		if env.FromName == "" {
			t.Fatalf("ParseEnvelope succeeded with no sender name for %q", content)
		}
	})
}
