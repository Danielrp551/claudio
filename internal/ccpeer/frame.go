package ccpeer

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// FrameVersion is the wire version this build speaks. Captured live from Claude
// Code 2.1.269. An unknown version is refused rather than guessed at, so the
// caller can degrade. See ADR-0008.
const FrameVersion = 1

// MaxMessageChars is the documented cap on the serialized form of a message.
// Claude Code refuses anything larger at the sender, before it leaves.
const MaxMessageChars = 1_000_000

// Priority controls where a message enters the receiver's queue.
type Priority string

// PriorityNext is the only value observed in captured traffic.
const PriorityNext Priority = "next"

// Frame is one line of the inbox protocol, as captured from a live session:
//
//	{"msgV":1,"msg_id":"...","type":"user",
//	 "message":{"role":"user","content":"<cross-session-message ...>...</cross-session-message>"},
//	 "priority":"next","from":"uds:..."}
type Frame struct {
	MsgV     int          `json:"msgV"`
	MsgID    string       `json:"msg_id"`
	Type     string       `json:"type"`
	Message  FrameMessage `json:"message"`
	Priority Priority     `json:"priority,omitempty"`

	// From is the reply address: the sender's own endpoint, prefixed with
	// "uds:". It is the same value shown in the Peer address row of /status.
	From string `json:"from,omitempty"`
}

// FrameMessage carries the text, wrapped in its attribution element.
type FrameMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Envelope is the content of a frame with its attribution element unwrapped.
type Envelope struct {
	// From is the sender's endpoint, including the "uds:" prefix.
	From string
	// FromName is the sender's session name, as ListAgents shows it.
	FromName string
	// FromMode is the sender's permission class, either "prompting" or the
	// value used for sessions that bypass permission prompts. It is what feeds
	// the receiver's inbound rules, and what this project propagates to defend
	// against permission laundering. See ADR-0006.
	FromMode string
	// Text is what the sending model actually wrote.
	Text string
}

var (
	envelopeRe = regexp.MustCompile(
		`(?s)^\s*<cross-session-message\s+([^>]*)>\n?(.*?)\n?</cross-session-message>\s*$`)
	attrRe = regexp.MustCompile(`([a-zA-Z-]+)="([^"]*)"`)
)

// ParseEnvelope unwraps the content of a frame.
//
// It fails when the attribution element is missing rather than returning bare
// text. In a workspace shared by several people, attribution is a security
// requirement and not decoration, so unattributed content must not flow on.
func ParseEnvelope(content string) (Envelope, error) {
	m := envelopeRe.FindStringSubmatch(content)
	if m == nil {
		return Envelope{}, fmt.Errorf(
			"ccpeer: content carries no cross-session-message element")
	}

	env := Envelope{Text: m[2]}
	for _, a := range attrRe.FindAllStringSubmatch(m[1], -1) {
		switch a[1] {
		case "from":
			env.From = unescapeAttr(a[2])
		case "from-name":
			env.FromName = unescapeAttr(a[2])
		case "from-mode":
			env.FromMode = unescapeAttr(a[2])
		}
	}
	if env.FromName == "" {
		return Envelope{}, fmt.Errorf(
			"ccpeer: frame carries no from-name, refusing to deliver unattributed content")
	}
	return env, nil
}

var attrUnescaper = strings.NewReplacer(
	"&quot;", `"`, "&amp;", "&", "&lt;", "<", "&gt;", ">")

func unescapeAttr(s string) string { return attrUnescaper.Replace(s) }

// DecodeFrame parses one line of the protocol.
//
// An unknown wire version is an error, not something to interpret optimistically.
// The caller is expected to degrade to the MCP transport when this happens.
func DecodeFrame(line []byte) (Frame, error) {
	var f Frame
	if err := json.Unmarshal(line, &f); err != nil {
		return Frame{}, fmt.Errorf("ccpeer: unreadable frame: %w", err)
	}
	if f.MsgV != FrameVersion {
		return Frame{}, fmt.Errorf(
			"ccpeer: unsupported frame version %d, this build speaks %d",
			f.MsgV, FrameVersion)
	}
	return f, nil
}
