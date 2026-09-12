package ccpeer

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Danielrp551/claudio/internal/safetext"
)

// DefaultDeliverTimeout bounds one delivery. Claude Code closes a connection
// that has not sent a complete line within thirty seconds, so a sender that
// takes longer than a few seconds is stuck rather than slow.
const DefaultDeliverTimeout = 10 * time.Second

// Client delivers frames into the inboxes of Claude Code sessions.
//
// Read the contract of Deliver before using it. It is the single most
// misunderstood thing in this protocol.
type Client struct {
	platform LocalEndpoint
	reg      *Registry
	timeout  time.Duration
	log      *slog.Logger
}

// NewClient returns a client that delivers through platform, reading keys from
// reg when the platform requires an authentication line.
func NewClient(platform LocalEndpoint, reg *Registry, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		platform: platform,
		reg:      reg,
		timeout:  DefaultDeliverTimeout,
		log:      log,
	}
}

// Deliver writes a frame into the inbox of target.
//
// A nil error means the bytes were accepted by the receiving process. **It does
// not mean the message reached the agent.** Claude Code discards a frame it does
// not accept without reporting anything back on the connection, which was
// verified in both directions: the same write succeeds whether or not the
// message is later delivered.
//
// Callers must therefore record a nil error as "handed over" and never as
// "delivered". See ADR-0007.
func (c *Client) Deliver(ctx context.Context, target Record, f Frame) error {
	if !target.Supported() {
		return fmt.Errorf("%w: %s declares peerProtocol %d, this build speaks %d",
			ErrUnsupportedProtocol, target.Name, target.PeerProtocol, PeerProtocol)
	}

	blob, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("ccpeer: encoding a frame: %w", err)
	}
	if len(blob) > MaxMessageChars {
		return fmt.Errorf("%w: %d bytes, the cap is %d", ErrMessageTooLarge, len(blob), MaxMessageChars)
	}

	var token string
	if c.platform.RequiresAuthLine() {
		token, err = c.reg.Token(target.PID)
		if err != nil {
			return fmt.Errorf("delivering to %s: %w", target.Name, err)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	conn, err := c.platform.Dial(ctx, target.MessagingSocketPath)
	if err != nil {
		return fmt.Errorf("delivering to %s: %w", target.Name, err)
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}

	w := bufio.NewWriter(conn)

	if token != "" {
		line, err := json.Marshal(authLine{Type: "auth", Token: token})
		if err != nil {
			return fmt.Errorf("ccpeer: encoding an authentication line: %w", err)
		}
		if _, err := w.Write(line); err != nil {
			return fmt.Errorf("delivering to %s: %w", target.Name, err)
		}
		if err := w.WriteByte('\n'); err != nil {
			return fmt.Errorf("delivering to %s: %w", target.Name, err)
		}
	}

	if _, err := w.Write(blob); err != nil {
		return fmt.Errorf("delivering to %s: %w", target.Name, err)
	}
	if err := w.WriteByte('\n'); err != nil {
		return fmt.Errorf("delivering to %s: %w", target.Name, err)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("delivering to %s: %w", target.Name, err)
	}

	c.log.Debug("frame handed to a session inbox",
		"target", target.Name, "pid", target.PID, "bytes", len(blob), "msg_id", f.MsgID)
	return nil
}

// FrameOptions describes a message to be wrapped and sent.
type FrameOptions struct {
	// Text is what the sender wrote.
	Text string
	// FromAddress is the reply address, which must be an inbox this process
	// owns and is listening on. It is not decoration: it is where the receiving
	// Claude Code sends its reply, and where notices about the fate of this
	// message arrive.
	FromAddress string
	// FromName is the name the message appears under in the conversation.
	FromName string
	// FromMode is the permission class of the sender. It is what the receiver's
	// inbound rules act on, and what defends against permission laundering, so
	// it is never invented by the sender of a workspace message.
	FromMode string
	// Priority defaults to PriorityNext.
	Priority Priority
}

// NewFrame wraps text in the attribution element and returns a frame ready to
// deliver.
func NewFrame(o FrameOptions) (Frame, error) {
	if o.FromAddress == "" {
		return Frame{}, errors.New("ccpeer: a frame needs a reply address")
	}
	if o.FromName == "" {
		return Frame{}, errors.New("ccpeer: a frame needs a sender name")
	}
	if o.FromMode == "" {
		o.FromMode = "prompting"
	}
	if o.Priority == "" {
		o.Priority = PriorityNext
	}

	// The reply address is built by this machine, from a path this machine
	// chose, so anything wrong with it is a bug here rather than an attack. It
	// is still checked, because it is about to be written into an attribute and
	// a newline in it would split the element in two.
	if err := safetext.CheckField("reply address", o.FromAddress); err != nil {
		return Frame{}, fmt.Errorf("ccpeer: %w", err)
	}

	// The sender name is a different matter. It carries the name of a person on
	// another machine, so it is cleaned rather than trusted.
	o.FromName = safetext.Field(o.FromName)
	o.FromMode = safetext.Field(o.FromMode)
	if o.FromName == "" {
		return Frame{}, errors.New("ccpeer: the sender name is empty once cleaned")
	}

	// The body is neutralised here as well as in the trust framing. The two
	// layers do not know about each other, and the operation is idempotent, so
	// running it twice costs nothing and forgetting it once would cost a lot.
	// This is the layer that matters for text that never passed through the
	// trust package, which includes everything at the peer level.
	o.Text = safetext.Body(o.Text)

	// The attribute values are quoted by hand rather than with %q on purpose.
	// A Windows inbox path is full of backslashes, and %q would escape every one
	// of them, producing an address the receiver cannot dial. Claude Code writes
	// these paths literally, so this does too, with only the characters that
	// would break the element itself escaped.
	//nolint:gocritic // %q would escape the backslashes in a Windows path, see above
	content := fmt.Sprintf(
		"<cross-session-message from=\"%s\" from-name=\"%s\" from-mode=\"%s\">\n%s\n</cross-session-message>",
		escapeAttr(o.FromAddress), escapeAttr(o.FromName), escapeAttr(o.FromMode), o.Text)

	return Frame{
		MsgV:     FrameVersion,
		MsgID:    newMessageID(),
		Type:     "user",
		Message:  FrameMessage{Role: "user", Content: content},
		Priority: o.Priority,
		From:     o.FromAddress,
	}, nil
}

// ReplyAddress prefixes an inbox path the way the protocol expects, which is the
// same form the Peer address row of /status shows.
func ReplyAddress(inboxPath string) string { return "uds:" + inboxPath }

var attrEscaper = strings.NewReplacer(
	"&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")

func escapeAttr(s string) string { return attrEscaper.Replace(s) }

func newMessageID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b)
}
