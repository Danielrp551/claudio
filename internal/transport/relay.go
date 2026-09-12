// Package transport carries messages between machines.
//
// The only implementation in version one is a client for a self hosted relay.
// It satisfies the interface the daemon declares, which is where the contract
// lives, because the daemon is the consumer. See ADR-0005 and ADR-0009.
package transport

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/Danielrp551/claudio/internal/daemon"
	"github.com/Danielrp551/claudio/internal/identity"
	"github.com/Danielrp551/claudio/internal/relay"
	"github.com/Danielrp551/claudio/internal/workspace"
)

const (
	minReconnect = time.Second
	maxReconnect = 30 * time.Second

	// pingInterval keeps a connection alive through the kind of middlebox that
	// silently drops an idle one.
	pingInterval = 45 * time.Second
)

// Options configures a relay client.
type Options struct {
	// URL is the relay endpoint, for example wss://relay.example.com/connect.
	URL string
	// Workspace is the slug to join.
	Workspace string
	// Identity is this member's key material.
	Identity *identity.Identity
	// Person is the display name published with this member.
	Person string
	// MachineID is stable for this machine, so a reconnect replaces its sessions
	// rather than adding a second set.
	MachineID string

	Logger *slog.Logger
}

// RelayClient connects a machine to a relay.
//
// It reconnects on its own with backoff, and it never surfaces a disconnection
// as a failure to the daemon. What it does surface is an accurate roster: while
// it is not connected, the roster is empty, so nothing looks reachable that is
// not.
type RelayClient struct {
	opts Options
	log  *slog.Logger

	deliveries chan daemon.Delivery

	mu       sync.RWMutex
	conn     *websocket.Conn
	members  map[string]relay.RosterMember  // member id to member
	sessions map[string]relay.RosterSession // session id to session
	exposed  []workspace.Session
	seq      map[string]int64 // session id to the next sequence number

	workspaceID string
	memberID    string

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// Dial starts a relay client. It returns as soon as the background connection
// loop is running, not when the connection is established, so a relay that is
// briefly unreachable does not stop a connector from starting.
func Dial(ctx context.Context, opts Options) (*RelayClient, error) {
	if opts.URL == "" {
		return nil, errors.New("transport: a relay URL is required")
	}
	if opts.Workspace == "" {
		return nil, errors.New("transport: a workspace is required")
	}
	if opts.Identity == nil {
		return nil, errors.New("transport: an identity is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	ctx, cancel := context.WithCancel(ctx)
	c := &RelayClient{
		opts:       opts,
		log:        opts.Logger,
		deliveries: make(chan daemon.Delivery, 64),
		members:    map[string]relay.RosterMember{},
		sessions:   map[string]relay.RosterSession{},
		seq:        map[string]int64{},
		ctx:        ctx,
		cancel:     cancel,
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.keepConnected()
	}()

	return c, nil
}

// Deliveries yields messages arriving from remote sessions.
func (c *RelayClient) Deliveries() <-chan daemon.Delivery { return c.deliveries }

// Roster returns the remote sessions currently reachable.
func (c *RelayClient) Roster() []daemon.RemoteSession {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]daemon.RemoteSession, 0, len(c.sessions))
	for _, s := range c.sessions {
		out = append(out, daemon.RemoteSession{
			ID:      s.ID,
			Person:  s.Person,
			Machine: s.Machine,
			Session: s.Name,
			Status:  s.Status,
		})
	}
	return out
}

// Expose tells the relay which local sessions this machine shares. It is safe to
// call before a connection exists, and the value is republished on every
// reconnect without the daemon having to ask again.
func (c *RelayClient) Expose(sessions []daemon.ExposedSession) {
	out := make([]workspace.Session, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, workspace.Session{
			ID: s.ID, Name: s.Name, CWD: s.CWD, Status: s.Status,
		})
	}

	c.mu.Lock()
	c.exposed = out
	conn := c.conn
	c.mu.Unlock()

	if conn != nil {
		c.publishSessions(c.ctx, conn)
	}
}

// Send seals a message for the member who owns the destination session and hands
// it to the relay.
//
// A nil error means the relay accepted the envelope. It does not mean anybody
// read it, and the daemon records it accordingly. See ADR-0007.
func (c *RelayClient) Send(ctx context.Context, msg daemon.Outbound) error {
	c.mu.RLock()
	session, known := c.sessions[msg.To]
	var recipient relay.RosterMember
	if known {
		recipient = c.members[session.MemberID]
	}
	conn := c.conn
	seq := c.seq[msg.To] + 1
	c.mu.RUnlock()

	if !known {
		return fmt.Errorf("transport: %q is not a session this workspace reaches", msg.To)
	}
	if !recipient.Identity.Valid() {
		return fmt.Errorf("transport: the owner of %q has no usable identity", msg.To)
	}
	if conn == nil {
		return errors.New("transport: not connected to the relay")
	}

	payload := relay.Payload{
		Text:        msg.Text,
		FromSession: msg.FromSession,
		FromMode:    msg.FromMode,
		ToSession:   msg.To,
		MsgID:       msg.MsgID,
		Seq:         seq,
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("transport: encoding a payload: %w", err)
	}

	envelope, err := c.opts.Identity.Seal(recipient.Identity, plaintext)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	err = wsjson.Write(ctx, conn, relay.Message{
		Type:        relay.TypeEnvelope,
		To:          recipient.Identity.Signing,
		ToSession:   msg.To,
		FromSession: msg.FromSession,
		Payload:     &envelope,
	})
	if err != nil {
		return fmt.Errorf("transport: sending an envelope: %w", err)
	}

	c.mu.Lock()
	c.seq[msg.To] = seq
	c.mu.Unlock()

	return nil
}

// Close stops the client and closes the delivery channel.
func (c *RelayClient) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.wg.Wait()
		close(c.deliveries)
	})
	return nil
}

// keepConnected owns the connection lifecycle. It is the only goroutine that
// assigns to c.conn.
func (c *RelayClient) keepConnected() {
	backoff := minReconnect

	for {
		if c.ctx.Err() != nil {
			return
		}

		err := c.runOnce(c.ctx)
		if c.ctx.Err() != nil {
			return
		}
		if err != nil {
			c.log.Warn("the relay connection dropped", "error", err, "retryIn", backoff)
		}

		// A dropped connection means nothing is reachable, and saying so is
		// better than leaving a stale roster that promises peers who are gone.
		c.mu.Lock()
		c.conn = nil
		c.sessions = map[string]relay.RosterSession{}
		c.mu.Unlock()

		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxReconnect {
			backoff *= 2
			if backoff > maxReconnect {
				backoff = maxReconnect
			}
		}
	}
}

func (c *RelayClient) runOnce(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, _, err := websocket.Dial(dialCtx, c.opts.URL, nil)
	cancel()
	if err != nil {
		return fmt.Errorf("transport: dialling the relay: %w", err)
	}
	conn.SetReadLimit(8 << 20)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	if err := c.handshake(ctx, conn); err != nil {
		return err
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	c.log.Info("connected to the relay", "url", c.opts.URL, "workspace", c.opts.Workspace)
	c.publishSessions(ctx, conn)

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	var pinger sync.WaitGroup
	pinger.Add(1)
	go func() {
		defer pinger.Done()
		t := time.NewTimer(pingInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = wsjson.Write(ctx, conn, relay.Message{Type: relay.TypePing})
				t.Reset(pingInterval)
			}
		}
	}()

	err = c.readLoop(ctx, conn)
	stop()
	pinger.Wait()
	return err
}

func (c *RelayClient) handshake(ctx context.Context, conn *websocket.Conn) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	pub := c.opts.Identity.Public()
	err := wsjson.Write(ctx, conn, relay.Message{
		Type:      relay.TypeHello,
		Workspace: c.opts.Workspace,
		Signing:   pub.Signing,
		Agreement: pub.Agreement,
		Person:    c.opts.Person,
	})
	if err != nil {
		return fmt.Errorf("transport: sending hello: %w", err)
	}

	var challenge relay.Message
	if err := wsjson.Read(ctx, conn, &challenge); err != nil {
		return fmt.Errorf("transport: reading the challenge: %w", err)
	}
	if challenge.Type == relay.TypeError {
		return fmt.Errorf("transport: the relay refused the connection: %s", challenge.Reason)
	}
	if challenge.Type != relay.TypeChallenge || len(challenge.Nonce) == 0 {
		return fmt.Errorf("transport: expected a challenge, got %q", challenge.Type)
	}

	var welcome relay.Message
	err = wsjson.Write(ctx, conn, relay.Message{
		Type:      relay.TypeAuth,
		Signature: c.opts.Identity.SignChallenge(c.opts.Workspace, challenge.Nonce),
	})
	if err != nil {
		return fmt.Errorf("transport: answering the challenge: %w", err)
	}
	if err := wsjson.Read(ctx, conn, &welcome); err != nil {
		return fmt.Errorf("transport: reading the welcome: %w", err)
	}
	if welcome.Type == relay.TypeError {
		return fmt.Errorf("transport: the relay refused the connection: %s", welcome.Reason)
	}
	if welcome.Type != relay.TypeWelcome {
		return fmt.Errorf("transport: expected a welcome, got %q", welcome.Type)
	}

	c.mu.Lock()
	c.workspaceID = welcome.WorkspaceID
	c.memberID = welcome.MemberID
	c.mu.Unlock()

	return nil
}

func (c *RelayClient) publishSessions(ctx context.Context, conn *websocket.Conn) {
	c.mu.RLock()
	exposed := make([]workspace.Session, len(c.exposed))
	copy(exposed, c.exposed)
	c.mu.RUnlock()

	host, _ := os.Hostname()
	err := wsjson.Write(ctx, conn, relay.Message{
		Type: relay.TypeSessions,
		Machine: &workspace.Machine{
			ID:       c.opts.MachineID,
			Hostname: host,
			OS:       runtime.GOOS,
		},
		Exposed: exposed,
	})
	if err != nil {
		c.log.Warn("could not publish the exposed sessions", "error", err)
	}
}

func (c *RelayClient) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		var msg relay.Message
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return err
		}

		switch msg.Type {
		case relay.TypeRoster:
			c.applyRoster(msg)
		case relay.TypeEnvelope:
			c.applyEnvelope(msg)
		case relay.TypeError:
			c.log.Warn("the relay reported a problem", "reason", msg.Reason)
		case relay.TypePing:
		}
	}
}

func (c *RelayClient) applyRoster(msg relay.Message) {
	members := make(map[string]relay.RosterMember, len(msg.Members))
	for _, m := range msg.Members {
		members[m.ID] = m
	}
	sessions := make(map[string]relay.RosterSession, len(msg.Peers))
	for _, s := range msg.Peers {
		sessions[s.ID] = s
	}

	c.mu.Lock()
	c.members = members
	c.sessions = sessions
	c.mu.Unlock()

	c.log.Debug("roster updated", "members", len(members), "sessions", len(sessions))
}

func (c *RelayClient) applyEnvelope(msg relay.Message) {
	if msg.Payload == nil {
		return
	}

	plaintext, senderKey, err := c.opts.Identity.Open(*msg.Payload)
	if err != nil {
		c.log.Warn("an envelope could not be opened", "error", err)
		return
	}

	// The envelope proves who sealed it. Whether that sender is somebody this
	// workspace knows is a separate question, and one worth answering, because
	// a signature from a stranger is still a valid signature.
	sender, ok := c.memberBySigning(senderKey)
	if !ok {
		c.log.Warn("an envelope arrived from somebody this workspace does not know",
			"sender", relay.EncodeKey(senderKey))
		return
	}

	var payload relay.Payload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		c.log.Warn("an envelope carried an unreadable payload", "error", err)
		return
	}

	from := daemon.RemoteSession{
		ID:      msg.FromSession,
		Person:  sender.Person,
		Session: payload.FromSession,
		Status:  "idle",
	}
	c.mu.RLock()
	if s, known := c.sessions[msg.FromSession]; known {
		from.Session = s.Name
		from.Machine = s.Machine
		from.Status = s.Status
	}
	c.mu.RUnlock()

	delivery := daemon.Delivery{
		From:          from,
		ToSession:     payload.ToSession,
		Text:          payload.Text,
		FromMode:      payload.FromMode,
		MsgID:         payload.MsgID,
		ProposedTrust: sender.Trust,
		Workspace:     c.opts.Workspace,
	}

	select {
	case c.deliveries <- delivery:
	case <-c.ctx.Done():
	}
}

func (c *RelayClient) memberBySigning(key ed25519.PublicKey) (relay.RosterMember, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, m := range c.members {
		if m.Identity.SameSigner(key) {
			return m, true
		}
	}
	return relay.RosterMember{}, false
}
