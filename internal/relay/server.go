package relay

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/danielrp551/claudio/internal/identity"
	"github.com/danielrp551/claudio/internal/workspace"
)

const (
	// handshakeTimeout bounds how long a connection may take to prove who it is.
	handshakeTimeout = 15 * time.Second

	// queueLimit is how many envelopes are held for a member who is offline.
	// Beyond it the oldest is dropped, because an unbounded queue is a way to
	// fill somebody else's disk.
	queueLimit = 500

	// queueTTL is how long an undelivered envelope is kept.
	queueTTL = 72 * time.Hour
)

// Server is the relay.
//
// It routes sealed envelopes between connectors and holds them for members who
// are offline. It knows who is connected and which envelope goes where, and it
// cannot read any of them.
type Server struct {
	store *workspace.Store
	log   *slog.Logger

	mu     sync.Mutex
	online map[string]*client  // member id to connection
	queued map[string][]queued // member id to envelopes waiting
}

type queued struct {
	msg Message
	at  time.Time
}

type client struct {
	memberID    string
	workspaceID string
	conn        *websocket.Conn

	// machineID is learned when the connector first reports what it exposes. It
	// is remembered so that a connector going away takes its sessions out of
	// everybody's roster rather than leaving them to look reachable.
	machineMu sync.Mutex
	machineID string

	// writeMu serialises writes, because the router and the connection's own
	// loop both send.
	writeMu sync.Mutex
}

// NewServer builds a relay over a workspace store.
func NewServer(store *workspace.Store, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		store:  store,
		log:    log,
		online: map[string]*client{},
		queued: map[string][]queued{},
	}
}

// Handler returns the HTTP handler to mount. Everything happens on one endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/connect", s.handleConnect)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// A connector is not a browser, so there is no origin to check against.
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.log.Warn("a connection could not be upgraded", "error", err)
		return
	}
	// A message is capped well below what Claude Code itself accepts, and the
	// envelope adds overhead, so this leaves room without allowing an
	// unbounded read.
	conn.SetReadLimit(8 << 20)

	ctx := r.Context()
	c, err := s.handshake(ctx, conn)
	if err != nil {
		s.log.Warn("a connection failed the handshake", "error", err)
		_ = wsjson.Write(ctx, conn, Message{Type: TypeError, Reason: err.Error()})
		_ = conn.Close(websocket.StatusPolicyViolation, "handshake failed")
		return
	}

	s.register(c)
	defer s.unregister(c)

	s.log.Info("a connector joined", "member", c.memberID, "workspace", c.workspaceID)

	if err := s.serve(ctx, c); err != nil && !isNormalClose(err) {
		s.log.Warn("a connector dropped", "member", c.memberID, "error", err)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// handshake proves who is connecting.
//
// The client says who it claims to be, the relay sends a fresh random challenge,
// and the client signs it. Nothing reusable ever travels, so recording the
// exchange grants nothing.
func (s *Server) handshake(ctx context.Context, conn *websocket.Conn) (*client, error) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	var hello Message
	if err := wsjson.Read(ctx, conn, &hello); err != nil {
		return nil, fmt.Errorf("reading hello: %w", err)
	}
	if hello.Type != TypeHello {
		return nil, fmt.Errorf("expected %q, got %q", TypeHello, hello.Type)
	}
	if len(hello.Signing) != ed25519.PublicKeySize {
		return nil, errors.New("the signing key is not usable")
	}

	ws, err := s.store.WorkspaceBySlug(hello.Workspace)
	if err != nil {
		// Deliberately vague. A stranger learns nothing about which workspaces
		// exist on this relay.
		return nil, errors.New("that workspace and member are not known here")
	}

	member, err := s.store.MemberBySigning(ws.ID, hello.Signing)
	if err != nil {
		return nil, errors.New("that workspace and member are not known here")
	}

	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating a challenge: %w", err)
	}
	if err := wsjson.Write(ctx, conn, Message{Type: TypeChallenge, Nonce: nonce}); err != nil {
		return nil, fmt.Errorf("sending the challenge: %w", err)
	}

	var auth Message
	if err := wsjson.Read(ctx, conn, &auth); err != nil {
		return nil, fmt.Errorf("reading the answer: %w", err)
	}
	if auth.Type != TypeAuth {
		return nil, fmt.Errorf("expected %q, got %q", TypeAuth, auth.Type)
	}
	// The challenge is bound to the slug the client asked for, which is the one
	// thing both sides know before the welcome is sent.
	if !identity.VerifyChallenge(member.Identity, hello.Workspace, nonce, auth.Signature) {
		return nil, errors.New("the challenge was not answered correctly")
	}

	c := &client{memberID: member.ID, workspaceID: ws.ID, conn: conn}
	if err := c.write(ctx, Message{
		Type: TypeWelcome, MemberID: member.ID, WorkspaceID: ws.ID,
	}); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Server) serve(ctx context.Context, c *client) error {
	s.sendRoster(ctx, c)
	s.flushQueue(ctx, c)

	for {
		var msg Message
		if err := wsjson.Read(ctx, c.conn, &msg); err != nil {
			return err
		}

		switch msg.Type {
		case TypeSessions:
			s.handleSessions(ctx, c, msg)
		case TypeEnvelope:
			s.handleEnvelope(ctx, c, msg)
		case TypePing:
			_ = c.write(ctx, Message{Type: TypePing})
		default:
			_ = c.write(ctx, Message{
				Type: TypeError, Reason: fmt.Sprintf("unknown message type %q", msg.Type),
			})
		}
	}
}

func (s *Server) handleSessions(ctx context.Context, c *client, msg Message) {
	machine := workspace.Machine{}
	if msg.Machine != nil {
		machine = *msg.Machine
	}
	if err := s.store.ReplaceSessions(c.memberID, machine, msg.Exposed); err != nil {
		s.log.Warn("could not record exposed sessions", "member", c.memberID, "error", err)
		return
	}
	if machine.ID != "" {
		c.setMachine(machine.ID)
	}
	// Everybody's view of who is reachable just changed.
	s.broadcastRoster(ctx, c.workspaceID)
}

func (s *Server) handleEnvelope(ctx context.Context, c *client, msg Message) {
	if msg.Payload == nil || len(msg.To) == 0 {
		_ = c.write(ctx, Message{Type: TypeError, Reason: "an envelope needs a recipient and a payload"})
		return
	}

	recipient, err := s.store.MemberBySigning(c.workspaceID, msg.To)
	if err != nil {
		_ = c.write(ctx, Message{Type: TypeError, Reason: "that recipient is not a member here"})
		return
	}

	sender, err := s.store.Member(c.memberID)
	if err != nil {
		return
	}

	out := Message{
		Type:        TypeEnvelope,
		From:        sender.Identity.Signing,
		FromSession: msg.FromSession,
		ToSession:   msg.ToSession,
		Payload:     msg.Payload,
	}

	s.mu.Lock()
	target, online := s.online[recipient.ID]
	s.mu.Unlock()

	if online {
		if err := target.write(ctx, out); err == nil {
			return
		}
		// The connection went away between the lookup and the write, so the
		// envelope goes to the queue like any other undelivered one.
	}
	s.enqueue(recipient.ID, out)
}

func (s *Server) enqueue(memberID string, msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	q := s.queued[memberID]
	if len(q) >= queueLimit {
		q = q[1:]
	}
	s.queued[memberID] = append(q, queued{msg: msg, at: time.Now()})
}

func (s *Server) flushQueue(ctx context.Context, c *client) {
	s.mu.Lock()
	pending := s.queued[c.memberID]
	delete(s.queued, c.memberID)
	s.mu.Unlock()

	cutoff := time.Now().Add(-queueTTL)
	for _, q := range pending {
		if q.at.Before(cutoff) {
			continue
		}
		if err := c.write(ctx, q.msg); err != nil {
			// Put back what could not be sent, so a connection that drops mid
			// flush does not lose the rest.
			s.enqueue(c.memberID, q.msg)
			return
		}
	}
}

func (s *Server) sendRoster(ctx context.Context, c *client) {
	msg := Message{Type: TypeRoster}
	for _, m := range s.store.ListMembers(c.workspaceID) {
		msg.Members = append(msg.Members, RosterMember{
			ID: m.ID, Person: m.Person, Role: m.Role,
			Trust: m.Trust, Status: m.Status, Identity: m.Identity,
		})
	}
	for _, sess := range s.store.Roster(c.workspaceID, c.memberID) {
		person := ""
		if owner, err := s.store.Member(sess.MemberID); err == nil {
			person = owner.Person
		}
		msg.Peers = append(msg.Peers, RosterSession{
			ID: sess.ID, MemberID: sess.MemberID, Person: person,
			Name: sess.Name, Status: sess.Status,
		})
	}
	_ = c.write(ctx, msg)
}

func (s *Server) broadcastRoster(ctx context.Context, workspaceID string) {
	s.mu.Lock()
	clients := make([]*client, 0, len(s.online))
	for _, c := range s.online {
		if c.workspaceID == workspaceID {
			clients = append(clients, c)
		}
	}
	s.mu.Unlock()

	for _, c := range clients {
		s.sendRoster(ctx, c)
	}
}

func (s *Server) register(c *client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// One connection per member. A second one replaces the first, which is what
	// a reconnect after a network drop looks like from here.
	if old, exists := s.online[c.memberID]; exists {
		_ = old.conn.Close(websocket.StatusPolicyViolation, "replaced by a newer connection")
	}
	s.online[c.memberID] = c
}

func (s *Server) unregister(c *client) {
	s.mu.Lock()
	if s.online[c.memberID] == c {
		delete(s.online, c.memberID)
	}
	s.mu.Unlock()

	// The sessions of a connector that went away stop being reachable.
	if id := c.machine(); id != "" {
		_ = s.store.DropMachine(id)
	}
	s.broadcastRoster(context.Background(), c.workspaceID)
}

func (c *client) setMachine(id string) {
	c.machineMu.Lock()
	defer c.machineMu.Unlock()
	c.machineID = id
}

func (c *client) machine() string {
	c.machineMu.Lock()
	defer c.machineMu.Unlock()
	return c.machineID
}

func (c *client) write(ctx context.Context, msg Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := wsjson.Write(ctx, c.conn, msg); err != nil {
		return fmt.Errorf("relay: writing to a connector: %w", err)
	}
	return nil
}

func isNormalClose(err error) bool {
	status := websocket.CloseStatus(err)
	return status == websocket.StatusNormalClosure ||
		status == websocket.StatusGoingAway ||
		errors.Is(err, context.Canceled)
}

// EncodeKey renders a public key for a log line or an error message.
func EncodeKey(k []byte) string { return base64.RawURLEncoding.EncodeToString(k) }
