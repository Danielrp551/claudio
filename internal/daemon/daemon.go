package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Danielrp551/claudio/internal/ccpeer"
	"github.com/Danielrp551/claudio/internal/trust"
)

// Promotion modes.
const (
	// ModeAuto promotes every reachable remote session up to the cap, evicting
	// the least recently used beyond it.
	ModeAuto = "auto"
	// ModeManual promotes only what the user asked for.
	ModeManual = "manual"
	// ModeOff promotes nothing. Everything goes through the workspace peer.
	ModeOff = "off"
)

// DefaultMaxGhosts is how many remote sessions get a native peer of their own
// before the least recently used is evicted.
//
// Eight is not arbitrary. A ghost costs about 6.4 MB of resident memory,
// measured rather than estimated, so eight is around 51 MB. The number belongs
// to the user and a workspace owner cannot raise it on somebody else's machine.
// See ADR-0003.
const DefaultMaxGhosts = 8

// DefaultPollInterval is how often local sessions and the remote roster are
// re-read.
//
// Polling rather than watching the filesystem is deliberate. A watcher is
// another dependency and behaves badly on network filesystems, while records
// change on the order of seconds. Boring is the right property here.
const DefaultPollInterval = 2 * time.Second

// Policy is the part of the configuration that belongs to this machine and is
// never synchronised with the workspace.
type Policy struct {
	Mode string `json:"mode"`
	// MaxGhosts is a pointer so that asking for none can be told apart from not
	// asking. With a plain integer, a configuration that never mentioned the cap
	// and one that set it to zero looked identical, and both quietly became
	// eight, so there was no way to say "run no extra processes".
	MaxGhosts *int `json:"maxGhosts,omitempty"`
	// Subscribed lists remote session identifiers to promote in manual mode.
	Subscribed []string `json:"subscribed,omitempty"`
	// IncludeWorkspaceInNames prefixes peer names with the workspace, which is
	// worth turning on once a person belongs to more than one.
	IncludeWorkspaceInNames bool `json:"includeWorkspaceInNames,omitempty"`
}

func (p Policy) withDefaults() Policy {
	if p.Mode == "" {
		p.Mode = ModeAuto
	}
	return p
}

// Cap is how many remote sessions get a native peer, with the default applied.
//
// Zero is a real answer and means none. A negative value is a typo rather than
// an intention, so it is treated as none too, because failing towards fewer
// processes is the safe direction on somebody else's machine.
func (p Policy) Cap() int {
	if p.MaxGhosts == nil {
		return DefaultMaxGhosts
	}
	if *p.MaxGhosts < 0 {
		return 0
	}
	return *p.MaxGhosts
}

// SetCap returns a copy with the cap set explicitly.
func (p Policy) SetCap(n int) Policy {
	p.MaxGhosts = &n
	return p
}

// TrustSettings is this machine's half of the trust decision.
//
// Everything here narrows. A workspace can propose a level and a member can be
// given one, but the values below are the ones their owner controls, and the
// effective level is the most restrictive of all of them. Nobody raises your
// level from somewhere else. See ADR-0006.
type TrustSettings struct {
	// Workspace applies to everybody in this workspace.
	Workspace string `json:"workspace,omitempty"`
	// People is keyed by the display name of a member.
	People map[string]string `json:"people,omitempty"`
	// Sessions is keyed by the name of a local Claude Code session, so the one
	// that touches production can be stricter than the rest of the machine.
	Sessions map[string]string `json:"sessions,omitempty"`
}

// For resolves the level that applies to one message.
func (t TrustSettings) For(proposed, person, localSession string) string {
	return trust.Resolve(
		proposed,
		t.Workspace,
		t.People[person],
		t.Sessions[localSession],
	)
}

// Options configures a daemon. Only Transport has no working default.
type Options struct {
	Platform     ccpeer.LocalEndpoint
	SessionsDirs []string
	StatePath    string
	Executable   string
	GhostArgs    []string
	Workspace    string
	Policy       Policy
	Trust        TrustSettings
	Transport    Transport
	Logger       *slog.Logger
	PollInterval time.Duration

	// MachineID makes an exposed session identifier stable across restarts.
	MachineID string

	// Exposes decides which local sessions this machine shares. A nil value
	// shares nothing, because exposure is opt in and defaulting to share would
	// be the wrong way round.
	Exposes func(name string) bool

	// StatusPath is where a snapshot is written after every poll, so the status
	// command can report what is running even when the connector is not.
	StatusPath string

	// HeldPath is where messages the hold gate withheld are kept, so a message
	// nobody has seen survives the connector restarting.
	HeldPath string

	// ControlAddressPath is where the connector publishes the path of its
	// control endpoint, so the MCP server and the command line can find it.
	ControlAddressPath string

	// Reload rereads the settings that belong to this machine, if the caller
	// provides it.
	//
	// It exists because trust used to be read once, at startup. Somebody who
	// lowered the level for a person saw no change and no explanation, and had to
	// know to restart the connector. Being told to restart a background process
	// before a security setting applies is the kind of thing that gets skipped.
	Reload func() (TrustSettings, Policy, error)
}

// Daemon is the connector that runs once per machine.
type Daemon struct {
	opts     Options
	platform ccpeer.LocalEndpoint
	registry *ccpeer.Registry
	client   *ccpeer.Client
	index    *orphanIndex
	held     *heldStore
	log      *slog.Logger

	mu        sync.Mutex
	local     []ccpeer.Record
	peers     map[string]string    // peer name to remote session id
	exposedTo map[string]string    // exposed session id to local session name
	lastUsed  map[string]time.Time // remote session id to last traffic
	degraded  string

	// sup is the running supervisor, so a control call can deliver a message
	// that was waiting for approval. It is nil until Run starts one.
	sup *supervisor

	// replyTo maps every way somebody might name a recent sender to an address
	// the transport accepts. It is what makes a conversation go both ways when
	// the person who started it exposes no session of their own.
	replyTo map[string]string
}

// New builds a daemon. It does not start anything.
func New(opts Options) (*Daemon, error) {
	if opts.Transport == nil {
		return nil, errors.New("daemon: a transport is required")
	}
	if opts.Platform == nil {
		opts.Platform = ccpeer.Platform()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.Executable == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("daemon: locating this executable: %w", err)
		}
		opts.Executable = exe
	}
	if len(opts.GhostArgs) == 0 {
		opts.GhostArgs = []string{"ghost"}
	}
	if opts.StatePath == "" {
		return nil, errors.New("daemon: a state path is required")
	}
	opts.Policy = opts.Policy.withDefaults()

	// Without an opinion of its own, a machine would let whatever the workspace
	// proposes stand, and a workspace proposing the highest level would then get
	// it everywhere. Starting from collaborator means raising the level takes
	// both sides, which is the rule in ADR-0006, and it holds whether or not the
	// command line remembered to set it.
	if opts.Trust.Workspace == "" {
		opts.Trust.Workspace = trust.Collaborator
	}

	registry, err := ccpeer.NewRegistry(opts.Platform, opts.SessionsDirs...)
	if err != nil {
		return nil, err
	}
	index, err := newOrphanIndex(opts.StatePath)
	if err != nil {
		return nil, err
	}
	held, heldErr := newHeldStore(opts.HeldPath)
	if held == nil {
		return nil, heldErr
	}

	d := &Daemon{
		opts:      opts,
		platform:  opts.Platform,
		registry:  registry,
		client:    ccpeer.NewClient(opts.Platform, registry, opts.Logger),
		index:     index,
		held:      held,
		log:       opts.Logger,
		peers:     map[string]string{},
		exposedTo: map[string]string{},
		lastUsed:  map[string]time.Time{},
		replyTo:   map[string]string{},
	}

	if heldErr != nil {
		d.log.Warn("could not read the messages held for approval", "error", heldErr)
	}

	if !opts.Platform.Verified() {
		d.degraded = fmt.Sprintf(
			"%s is not a verified platform, so no native peers are published and outbound "+
				"messages go through the MCP transport", opts.Platform.GOOS())
		d.log.Warn("running degraded", "reason", d.degraded)
	}

	return d, nil
}

// Run blocks until the context is cancelled, then stops everything it started.
func (d *Daemon) Run(ctx context.Context) error {
	removed, err := d.index.Sweep(d.platform)
	if err != nil {
		d.log.Warn("could not fully sweep records from a previous run", "error", err)
	}
	if removed > 0 {
		d.log.Warn("removed records left by a previous run that did not shut down cleanly",
			"count", removed)
	}

	sup := newSupervisor(ctx, d.opts.Executable, d.opts.GhostArgs,
		d.primarySessionsDir(), d.platform, d.index, d.log)

	d.mu.Lock()
	d.sup = sup
	d.mu.Unlock()

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		d.routeOutbound(ctx, sup)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		d.routeInbound(ctx, sup)
	}()

	if d.opts.ControlAddressPath != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.serveControl(ctx, d.opts.ControlAddressPath); err != nil {
				d.log.Error("the control endpoint stopped", "error", err)
			}
		}()
	}

	d.log.Info("connector is up",
		"workspace", d.opts.Workspace,
		"platform", d.platform.GOOS(),
		"sessions", d.registry.Dirs(),
		"mode", d.opts.Policy.Mode,
		"maxGhosts", d.opts.Policy.Cap())

	d.poll(ctx, sup)

	// Stop the supervisor first, so its frame channel closes and the outbound
	// router can finish, then wait for both routers.
	if err := sup.Close(); err != nil {
		d.log.Warn("closing the supervisor", "error", err)
	}
	if err := d.opts.Transport.Close(); err != nil {
		d.log.Warn("closing the transport", "error", err)
	}
	wg.Wait()

	d.log.Info("connector stopped")
	return nil
}

func (d *Daemon) primarySessionsDir() string {
	dirs := d.registry.Dirs()
	if len(dirs) == 0 {
		return ""
	}
	return dirs[0]
}

// poll refreshes what this machine knows and reconciles the set of native peers.
func (d *Daemon) poll(ctx context.Context, sup *supervisor) {
	t := time.NewTimer(0)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.reloadSettings()
			d.refreshLocal()
			d.publishExposed()
			d.reconcile(sup)
			d.writeStatus(sup)
			t.Reset(d.opts.PollInterval)
		}
	}
}

// reloadSettings picks up changes a person made while this was running.
//
// Only the settings that belong to this machine are reread. The workspace, the
// relay and the identity are what the connector connected with, and changing
// those means starting again.
func (d *Daemon) reloadSettings() {
	if d.opts.Reload == nil {
		return
	}

	trustSettings, policy, err := d.opts.Reload()
	if err != nil {
		d.log.Warn("could not reread the configuration", "error", err)
		return
	}
	if trustSettings.Workspace == "" {
		// Same reasoning as in New: without an opinion of its own, a machine
		// would let whatever the workspace proposes stand.
		trustSettings.Workspace = trust.Collaborator
	}

	d.mu.Lock()
	changed := d.opts.Policy.Mode != policy.Mode || d.opts.Policy.Cap() != policy.Cap()
	d.opts.Trust = trustSettings
	d.opts.Policy = policy.withDefaults()
	d.mu.Unlock()

	if changed {
		d.log.Info("the promotion policy changed",
			"mode", policy.Mode, "maxGhosts", policy.Cap())
	}
}

func (d *Daemon) refreshLocal() {
	sessions, err := d.registry.List()
	if err != nil {
		d.log.Warn("could not read local sessions", "error", err)
		return
	}
	// The ghosts this daemon runs publish records too, and they are not Claude
	// Code sessions. Filtering them out keeps the local view honest.
	ours := map[int]bool{}
	for _, e := range d.index.Tracked() {
		ours[e.PID] = true
	}
	kept := sessions[:0]
	for _, s := range sessions {
		if !ours[s.PID] {
			kept = append(kept, s)
		}
	}

	d.mu.Lock()
	d.local = kept
	d.mu.Unlock()
}

// publishExposed tells the transport which local sessions this machine shares.
func (d *Daemon) publishExposed() {
	if d.opts.Exposes == nil {
		return
	}

	d.mu.Lock()
	local := make([]ccpeer.Record, len(d.local))
	copy(local, d.local)
	d.mu.Unlock()

	exposed := make([]ExposedSession, 0, len(local))
	index := make(map[string]string, len(local))
	for _, s := range local {
		if !d.opts.Exposes(s.Name) {
			continue
		}
		id := d.opts.MachineID + ":" + s.Name
		exposed = append(exposed, ExposedSession{
			ID: id, Name: s.Name, CWD: s.CWD, Status: s.Status,
		})
		index[id] = s.Name
	}

	d.mu.Lock()
	d.exposedTo = index
	d.mu.Unlock()

	d.opts.Transport.Expose(exposed)
}

// writeStatus leaves a snapshot on disk for the status command.
//
// A file rather than another endpoint. The status command is allowed to be a
// poll interval behind, and not adding a second thing to bind and secure is
// worth more than the freshness.
func (d *Daemon) writeStatus(sup *supervisor) {
	if d.opts.StatusPath == "" {
		return
	}

	status := d.Snapshot()
	status.Peers = sup.Peers()

	blob, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return
	}
	tmp := d.opts.StatusPath + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, d.opts.StatusPath); err != nil {
		os.Remove(tmp)
	}
}

// reconcile makes the running peers match what the policy says they should be.
func (d *Daemon) reconcile(sup *supervisor) {
	if d.degraded != "" {
		return
	}

	want := d.wanted()

	running := map[string]bool{}
	for _, p := range sup.Peers() {
		running[p.Name] = true
	}

	for name := range want {
		if !running[name] {
			if err := sup.Ensure(name); err != nil {
				d.log.Warn("could not start a native peer", "peer", name, "error", err)
			}
		}
	}
	for name := range running {
		if _, keep := want[name]; !keep {
			d.log.Info("releasing a native peer", "peer", name)
			sup.Release(name)
		}
	}

	d.mu.Lock()
	d.peers = want
	d.mu.Unlock()
}

// wanted computes the peer names this machine should be running, mapped to the
// remote session each one stands for. The workspace peer maps to the empty
// string, because it stands for all of them.
func (d *Daemon) wanted() map[string]string {
	// Nothing is published until the transport has been accepted at least once.
	//
	// A peer in somebody's agent list is a promise that messages sent to it go
	// somewhere. A connector whose identity the relay refuses publishes one
	// anyway otherwise, and every session on the machine is offered a workspace
	// it cannot reach. Once a connection has succeeded the peers stay through a
	// disconnection, because a relay that is briefly away is a different thing
	// from a workspace this machine was never part of, and status says which.
	if !d.opts.Transport.Health().EverConnected {
		return map[string]string{}
	}

	// Off means no processes. The workspace peer used to be added before the mode
	// was looked at, so somebody who asked for none got one.
	if d.opts.Policy.Mode == ModeOff {
		return map[string]string{}
	}

	want := map[string]string{
		WorkspacePeerName(d.opts.Workspace): "",
	}

	roster := d.opts.Transport.Roster()
	candidates := make([]RemoteSession, 0, len(roster))

	switch d.opts.Policy.Mode {
	case ModeManual:
		subscribed := map[string]bool{}
		for _, id := range d.opts.Policy.Subscribed {
			subscribed[id] = true
		}
		for _, s := range roster {
			if subscribed[s.ID] {
				candidates = append(candidates, s)
			}
		}
	default:
		candidates = append(candidates, roster...)
	}

	// Beyond the cap, the least recently used loses its process. Nothing becomes
	// unreachable, because the workspace peer still carries it.
	if len(candidates) > d.opts.Policy.Cap() {
		d.mu.Lock()
		used := make(map[string]time.Time, len(d.lastUsed))
		for k, v := range d.lastUsed {
			used[k] = v
		}
		d.mu.Unlock()

		sort.SliceStable(candidates, func(i, j int) bool {
			return used[candidates[i].ID].After(used[candidates[j].ID])
		})
		candidates = candidates[:d.opts.Policy.Cap()]
	}

	for _, s := range candidates {
		want[PeerName(d.opts.Workspace, s, d.opts.Policy.IncludeWorkspaceInNames)] = s.ID
	}
	return want
}

// routeOutbound turns frames written by local Claude Code sessions into messages
// for the transport.
func (d *Daemon) routeOutbound(ctx context.Context, sup *supervisor) {
	for in := range sup.Frames() {
		env, err := ccpeer.ParseEnvelope(in.Frame.Message.Content)
		if err != nil {
			d.log.Warn("dropped a frame with no attribution", "peer", in.Peer, "error", err)
			continue
		}

		target, text := d.resolveTarget(in.Peer, env.Text)
		if target == "" {
			d.log.Warn("could not work out who a message was for",
				"peer", in.Peer, "msg_id", in.Frame.MsgID)
			continue
		}

		d.mu.Lock()
		d.lastUsed[target] = time.Now()
		d.mu.Unlock()

		msg := Outbound{
			To:          target,
			Text:        text,
			FromSession: env.FromName,
			FromMode:    env.FromMode,
			MsgID:       in.Frame.MsgID,
		}
		if err := d.opts.Transport.Send(ctx, msg); err != nil {
			d.log.Error("could not send a message",
				"to", target, "msg_id", msg.MsgID, "error", err)
			continue
		}
		d.log.Info("message handed to the transport",
			"to", target, "from", env.FromName, "msg_id", msg.MsgID)
	}
}

// resolveTarget works out which remote session a frame is for.
//
// A frame that arrived at a session peer is for that session. A frame that
// arrived at the workspace peer carries its destination in the text, as a
// leading mention, because a single peer stands for everybody.
func (d *Daemon) resolveTarget(peer, text string) (target, body string) {
	d.mu.Lock()
	id, known := d.peers[peer]
	d.mu.Unlock()

	if known && id != "" {
		return id, text
	}

	mention, rest := splitMention(text)
	if mention == "" {
		return "", text
	}
	for _, s := range d.opts.Transport.Roster() {
		if matchesMention(s, mention) {
			return s.ID, rest
		}
	}

	// Nobody in the roster answers to that name. Somebody who wrote recently
	// might, and they are reachable precisely because they wrote.
	d.mu.Lock()
	addr, known := d.replyTo[strings.ToLower(mention)]
	d.mu.Unlock()
	if known {
		return addr, rest
	}
	return "", text
}

// rememberSender records how to answer somebody who just wrote.
//
// Every name the message could be answered by is recorded, because the person
// replying writes a name rather than an identifier: the session, the person, and
// both of the ways a peer is named. The map only grows with the number of people
// who have actually written, which is the size of the workspace at worst.
func (d *Daemon) rememberSender(msg Delivery) {
	if msg.ReplyTo == "" {
		return
	}

	names := []string{
		msg.From.Session,
		msg.From.Person,
		msg.From.Person + "/" + msg.From.Session,
		msg.From.Person + "-" + msg.From.Session,
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, n := range names {
		if n != "" && n != "/" && n != "-" {
			d.replyTo[strings.ToLower(n)] = msg.ReplyTo
		}
	}
}

// splitMention pulls a leading destination out of a message written to the
// workspace peer, in the shape "@person/session: the actual message".
func splitMention(text string) (mention, rest string) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "@") {
		return "", text
	}
	trimmed = trimmed[1:]

	cut := strings.IndexAny(trimmed, ": \n")
	if cut < 0 {
		return trimmed, ""
	}
	mention = trimmed[:cut]
	rest = strings.TrimLeft(trimmed[cut:], ": \n")
	return mention, rest
}

func matchesMention(s RemoteSession, mention string) bool {
	candidates := []string{
		s.ID,
		s.Person + "/" + s.Session,
		s.Person + "-" + s.Session,
		s.Session,
	}
	for _, c := range candidates {
		if strings.EqualFold(c, mention) {
			return true
		}
	}
	return false
}

// routeInbound delivers messages from remote sessions into the inbox of the
// local Claude Code session they are addressed to.
func (d *Daemon) routeInbound(ctx context.Context, sup *supervisor) {
	for msg := range d.opts.Transport.Deliveries() {
		if err := d.deliver(ctx, sup, msg, false); err != nil {
			d.log.Error("could not deliver an incoming message",
				"from", msg.From.ID, "to", msg.ToSession, "msg_id", msg.MsgID, "error", err)
			continue
		}
		d.mu.Lock()
		d.lastUsed[msg.From.ID] = time.Now()
		d.mu.Unlock()
	}
}

// deliver puts one message into a local session's inbox.
//
// approved says the person has already decided about this one, which is what an
// approval is, so the hold gate does not apply to it a second time.
func (d *Daemon) deliver(ctx context.Context, sup *supervisor, msg Delivery, approved bool) error {
	// A message is addressed to the identifier this machine published, so it is
	// translated back to the session name before anything is looked up.
	d.mu.Lock()
	name, known := d.exposedTo[msg.ToSession]
	d.mu.Unlock()
	if !known {
		name = msg.ToSession
	}

	target, err := d.localSession(name)
	if err != nil {
		return err
	}

	// The reply address has to be an endpoint this machine owns and listens on,
	// because it is where the receiving Claude Code sends its reply and where
	// notices about this message arrive. The peer that stands for the sender is
	// exactly that endpoint.
	peerName := PeerName(d.opts.Workspace, msg.From, d.opts.Policy.IncludeWorkspaceInNames)
	endpoint := d.endpointOf(sup, peerName)
	if endpoint == "" {
		peerName = WorkspacePeerName(d.opts.Workspace)
		endpoint = d.endpointOf(sup, peerName)
	}
	if endpoint == "" {
		return errors.New("no native peer is available to carry the reply address")
	}

	// The level is resolved here, on the receiving machine, from every opinion
	// that applies. What it changes is the framing the text arrives in, which is
	// the only thing that actually governs how the receiving model treats it.
	// Permissions are untouched at every level.
	d.rememberSender(msg)

	level := d.opts.Trust.For(msg.ProposedTrust, msg.From.Person, target.Name)
	if level == trust.Hold && approved {
		// Approved, so the gate is spent. The framing falls back to the default,
		// because hold says nothing about how to read a message, only about when.
		level = trust.Default
	}
	if level == trust.Hold {
		id, err := d.held.Add(msg)
		if err != nil {
			return err
		}
		d.log.Info("a message is waiting for approval",
			"id", id, "from", msg.From.Person, "session", target.Name, "msg_id", msg.MsgID)
		return nil
	}
	framed, err := trust.Frame(level, trust.Sender{
		Person:    msg.From.Person,
		Session:   msg.From.Session,
		Workspace: cmpOr(msg.Workspace, d.opts.Workspace),
		Mode:      msg.FromMode,
	}, msg.Text)
	if err != nil {
		return err
	}

	frame, err := ccpeer.NewFrame(ccpeer.FrameOptions{
		Text:        framed,
		FromAddress: ccpeer.ReplyAddress(endpoint),
		FromName:    peerName,
		FromMode:    msg.FromMode,
	})
	if err != nil {
		return err
	}

	if err := d.client.Deliver(ctx, target, frame); err != nil {
		return err
	}

	// A successful write proves the bytes were accepted and nothing more, so
	// this says handed over rather than delivered. See ADR-0007.
	d.log.Info("message handed to a local session inbox",
		"session", target.Name, "from", peerName, "trust", level, "msg_id", msg.MsgID)
	return nil
}

func newMessageID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("msg-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func cmpOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// localSession finds a local Claude Code session by name, or the only one there
// is when no name is given.
//
// A miss is not believed on the first answer. The cached view of local sessions
// is at most one poll interval old, so a session that started inside that window
// is real and simply not in it yet. Dropping a message for a session that does
// exist would be a failure the person who sent it can neither see nor do
// anything about, and being sure costs one directory listing on a path that has
// already gone wrong.
// approveByID delivers a message that was waiting, at whatever level applies now.
//
// The level is resolved again rather than remembered. Holding a message means
// the person had not decided, and by the time they approve it they have, so the
// framing reflects the decision rather than the uncertainty.
func (d *Daemon) approveByID(ctx context.Context, id string) error {
	d.mu.Lock()
	sup := d.sup
	d.mu.Unlock()
	if sup == nil {
		return errors.New("daemon: the connector is not running yet")
	}

	item, err := d.held.Take(id)
	if err != nil {
		return err
	}
	if err := d.deliver(ctx, sup, item.Delivery, true); err != nil {
		// Put it back rather than lose it. A delivery that failed is not a
		// decision the person can be asked to make again from nothing.
		if _, addErr := d.held.Add(item.Delivery); addErr != nil {
			d.log.Error("a held message was lost after a failed delivery",
				"id", id, "error", addErr)
		}
		return err
	}
	d.log.Info("a held message was approved and delivered", "id", id)
	return nil
}

func (d *Daemon) localSession(name string) (ccpeer.Record, error) {
	rec, err := d.lookupLocal(name)
	if err == nil {
		return rec, nil
	}
	d.refreshLocal()
	return d.lookupLocal(name)
}

func (d *Daemon) lookupLocal(name string) (ccpeer.Record, error) {
	d.mu.Lock()
	sessions := make([]ccpeer.Record, len(d.local))
	copy(sessions, d.local)
	d.mu.Unlock()

	if name != "" {
		for _, s := range sessions {
			if s.Name == name {
				return s, nil
			}
		}
		return ccpeer.Record{}, fmt.Errorf("%w: local session %q", ccpeer.ErrNotFound, name)
	}
	if len(sessions) == 1 {
		return sessions[0], nil
	}
	return ccpeer.Record{}, fmt.Errorf(
		"%w: no session named and %d are running", ccpeer.ErrAmbiguous, len(sessions))
}

func (d *Daemon) endpointOf(sup *supervisor, peer string) string {
	for _, p := range sup.Peers() {
		if p.Name == peer && p.Ready {
			return p.Endpoint
		}
	}
	return ""
}

// Status is what claudio status reports.
type Status struct {
	Workspace   string            `json:"workspace"`
	Platform    string            `json:"platform"`
	Verified    bool              `json:"verified"`
	Degraded    string            `json:"degraded,omitempty"`
	Transport   TransportHealth   `json:"transport"`
	SessionDirs []string          `json:"sessionDirs"`
	Policy      Policy            `json:"policy"`
	Local       []LocalSession    `json:"localSessions"`
	Remote      []RemoteSession   `json:"remoteSessions"`
	Peers       []PeerStatus      `json:"nativePeers"`
	Tracked     map[string]string `json:"trackedGhosts,omitempty"`
}

// LocalSession is a Claude Code session on this machine, as status reports it.
type LocalSession struct {
	Name   string `json:"name"`
	PID    int    `json:"pid"`
	CWD    string `json:"cwd"`
	Status string `json:"status"`
}

// Snapshot returns what this daemon currently knows.
func (d *Daemon) Snapshot() Status {
	d.mu.Lock()
	local := make([]LocalSession, 0, len(d.local))
	for _, s := range d.local {
		local = append(local, LocalSession{
			Name: s.Name, PID: s.PID, CWD: s.CWD, Status: s.Status,
		})
	}
	degraded := d.degraded
	d.mu.Unlock()

	return Status{
		Workspace:   d.opts.Workspace,
		Transport:   d.opts.Transport.Health(),
		Platform:    d.platform.GOOS(),
		Verified:    d.platform.Verified(),
		Degraded:    degraded,
		SessionDirs: d.registry.Dirs(),
		Policy:      d.opts.Policy,
		Local:       local,
		Remote:      d.opts.Transport.Roster(),
	}
}
