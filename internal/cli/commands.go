package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Danielrp551/claudio/internal/ccpeer"
	"github.com/Danielrp551/claudio/internal/config"
	"github.com/Danielrp551/claudio/internal/daemon"
	"github.com/Danielrp551/claudio/internal/identity"
	"github.com/Danielrp551/claudio/internal/relay"
	"github.com/Danielrp551/claudio/internal/transport"
	"github.com/Danielrp551/claudio/internal/trust"
	"github.com/Danielrp551/claudio/internal/workspace"
)

// runDaemon is the connector for this machine.
func runDaemon(ctx context.Context, env Env, args []string) error {
	fs := flagSet("daemon", env.Stderr)
	level := fs.String("log-level", "", "debug, info, warn, or error")
	if err := parse(fs, args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Ready(); err != nil {
		return err
	}
	log := newLogger(env.Stderr, cmpOr(*level, cfg.LogLevel))

	id, err := loadIdentity()
	if err != nil {
		return err
	}

	client, err := transport.Dial(ctx, transport.Options{
		URL:       cfg.Relay,
		Workspace: cfg.Workspace,
		Identity:  id,
		Person:    cfg.Person,
		MachineID: cfg.MachineID,
		Logger:    log,
	})
	if err != nil {
		return err
	}

	ghosts, err := config.Path(config.FileGhosts)
	if err != nil {
		return err
	}
	status, err := config.Path(config.FileStatus)
	if err != nil {
		return err
	}
	controlAddr, err := config.Path(daemon.AddressFile)
	if err != nil {
		return err
	}

	d, err := daemon.New(daemon.Options{
		SessionsDirs:       cfg.SessionsDirs,
		StatePath:          ghosts,
		StatusPath:         status,
		ControlAddressPath: controlAddr,
		Workspace:          cfg.Workspace,
		MachineID:          cfg.MachineID,
		Policy:             cfg.Policy,
		Trust:              cfg.Trust,
		Transport:          client,
		Exposes:            cfg.Exposes,
		Logger:             log,
		Reload: func() (daemon.TrustSettings, daemon.Policy, error) {
			fresh, err := config.Load()
			if err != nil {
				return daemon.TrustSettings{}, daemon.Policy{}, err
			}
			return fresh.Trust, fresh.Policy, nil
		},
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "claudio is connecting to %s as %s in %s\n",
		cfg.Relay, cfg.Person, cfg.Workspace)
	if len(cfg.Expose) == 0 {
		fmt.Fprintln(env.Stdout,
			"no sessions are shared yet, run \"claudio expose <session>\" to share one")
	}

	return d.Run(ctx)
}

// runRelay serves a workspace.
func runRelay(ctx context.Context, env Env, args []string) error {
	fs := flagSet("relay", env.Stderr)
	addr := fs.String("addr", ":8787", "address to listen on")
	storePath := fs.String("store", "", "where to keep the workspace, defaults to the configuration directory")
	level := fs.String("log-level", "info", "debug, info, warn, or error")
	if err := parse(fs, args); err != nil {
		return err
	}

	path, err := storeOrDefault(*storePath)
	if err != nil {
		return err
	}
	store, err := workspace.OpenStore(path)
	if err != nil {
		return err
	}
	log := newLogger(env.Stderr, *level)

	server := &http.Server{
		Addr:              *addr,
		Handler:           relay.NewServer(store, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", *addr)
	if err != nil {
		return fmt.Errorf("relay: listening on %s: %w", *addr, err)
	}

	fmt.Fprintf(env.Stdout, "relay listening on %s, workspace store at %s\n",
		listener.Addr(), path)

	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("relay: %w", err)
	}
}

// runWorkspace creates and inspects workspaces, on the machine where the store
// lives.
func runWorkspace(_ context.Context, env Env, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: claudio workspace create <slug> [--name <name>]")
	}

	switch args[0] {
	case "create":
		fs := flagSet("workspace create", env.Stderr)
		name := fs.String("name", "", "a readable name, defaults to the slug")
		storePath := fs.String("store", "", "where to keep the workspace")
		if err := parse(fs, args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: claudio workspace create <slug>")
		}
		slug := fs.Arg(0)

		path, err := storeOrDefault(*storePath)
		if err != nil {
			return err
		}
		store, err := workspace.OpenStore(path)
		if err != nil {
			return err
		}
		id, err := loadIdentity()
		if err != nil {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		ws, owner, err := relay.CreateWorkspace(store, slug, *name, cfg.Person, id.Public())
		if err != nil {
			return err
		}

		fmt.Fprintf(env.Stdout, "created the workspace %s\n", ws.Slug)
		fmt.Fprintf(env.Stdout, "  you are the owner, as %s\n", owner.Person)
		fmt.Fprintf(env.Stdout, "  your fingerprint is %s\n", id.Fingerprint())
		fmt.Fprintf(env.Stdout, "  the store is at %s\n", path)
		fmt.Fprintln(env.Stdout, "\nnext: run \"claudio relay\" here, then \"claudio invite\" to add somebody")
		return nil

	case "list":
		fs := flagSet("workspace list", env.Stderr)
		storePath := fs.String("store", "", "where the workspace is kept")
		if err := parse(fs, args[1:]); err != nil {
			return err
		}
		path, err := storeOrDefault(*storePath)
		if err != nil {
			return err
		}
		store, err := workspace.OpenStore(path)
		if err != nil {
			return err
		}
		for _, w := range store.Workspaces {
			fmt.Fprintf(env.Stdout, "%-20s %s\n", w.Slug, w.Name)
			for _, m := range store.ListMembers(w.ID) {
				marker := " "
				if m.Status != workspace.StatusActive {
					marker = "x"
				}
				fmt.Fprintf(env.Stdout, "  %s %-16s %-10s %-12s %s\n",
					marker, m.Person, m.Role, m.Trust, m.Identity.Fingerprint())
			}
		}
		return nil

	default:
		return fmt.Errorf("unknown workspace command %q, use create or list", args[0])
	}
}

// runInvite creates an invitation, on the machine where the store lives.
func runInvite(_ context.Context, env Env, args []string) error {
	fs := flagSet("invite", env.Stderr)
	slug := fs.String("workspace", "", "the workspace to invite into")
	storePath := fs.String("store", "", "where the workspace is kept")
	ttl := fs.Duration("expires-in", 24*time.Hour, "how long the code stays usable")
	uses := fs.Int("uses", 1, "how many times it can be redeemed, zero for no limit")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *slug == "" {
		return errors.New("claudio invite needs --workspace")
	}

	path, err := storeOrDefault(*storePath)
	if err != nil {
		return err
	}
	store, err := workspace.OpenStore(path)
	if err != nil {
		return err
	}

	code, err := relay.CreateInvitation(store, *slug, *ttl, *uses)
	if err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "%s\n\n", code)
	fmt.Fprintln(env.Stdout, "hand that to the person joining. They run:")
	fmt.Fprintf(env.Stdout, "  claudio join %s --relay wss://your-relay/connect --workspace %s\n",
		code, *slug)
	fmt.Fprintln(env.Stdout, "\nthe code is a voucher, not a password. It is redeemed once for a")
	fmt.Fprintln(env.Stdout, "membership with its own key, which you can revoke on its own.")
	return nil
}

// runJoin redeems an invitation and writes this machine's configuration.
func runJoin(ctx context.Context, env Env, args []string) error {
	fs := flagSet("join", env.Stderr)
	relayURL := fs.String("relay", "", "the relay endpoint, for example wss://relay.example.com/connect")
	slug := fs.String("workspace", "", "the workspace to join")
	person := fs.String("as", "", "the name other members see, defaults to your user name")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: claudio join <code> --relay <url> --workspace <slug>")
	}
	code := fs.Arg(0)
	if *relayURL == "" {
		return errors.New("claudio join needs --relay")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *person != "" {
		cfg.Person = *person
	}

	id, err := loadIdentity()
	if err != nil {
		return err
	}

	body, err := json.Marshal(relay.JoinRequest{
		Workspace: *slug,
		Code:      code,
		Person:    cfg.Person,
		Identity:  id.Public(),
	})
	if err != nil {
		return fmt.Errorf("claudio: encoding the request: %w", err)
	}

	endpoint := joinURL(*relayURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("claudio: building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("claudio: reaching the relay at %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("claudio: the relay refused the invitation (%s)", resp.Status)
	}

	var out relay.JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("claudio: reading the answer: %w", err)
	}

	cfg.Workspace = out.Workspace
	cfg.Relay = *relayURL
	if err := cfg.Save(); err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "joined %s as %s\n", out.Workspace, cfg.Person)
	fmt.Fprintf(env.Stdout, "  your fingerprint is %s\n", out.Fingerprint)
	fmt.Fprintf(env.Stdout, "  the workspace proposes trust level %q for you\n", out.Trust)
	fmt.Fprintln(env.Stdout, "\nnext: \"claudio expose <session>\" to share a session, then \"claudio daemon\"")
	return nil
}

// joinURL turns a WebSocket connect endpoint into the join endpoint beside it,
// so a person only has to be given one address.
func joinURL(relayURL string) string {
	u := relayURL
	u = strings.Replace(u, "wss://", "https://", 1)
	u = strings.Replace(u, "ws://", "http://", 1)
	u = strings.TrimSuffix(u, "/connect")
	return strings.TrimSuffix(u, "/") + "/join"
}

// runExpose chooses which local sessions this machine shares.
func runExpose(_ context.Context, env Env, args []string) error {
	// Parsed rather than taken literally. Without this, "claudio expose --help"
	// registered a session named --help in the configuration and shared it.
	fs := flagSet("expose", env.Stderr)
	if err := parse(fs, args); err != nil {
		return err
	}
	args = fs.Args()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		if len(cfg.Expose) == 0 {
			fmt.Fprintln(env.Stdout, "no sessions are shared")
			return nil
		}
		for _, name := range cfg.Expose {
			fmt.Fprintln(env.Stdout, name)
		}
		return nil
	}

	for _, name := range args {
		if !cfg.Exposes(name) {
			cfg.Expose = append(cfg.Expose, name)
		}
	}
	sort.Strings(cfg.Expose)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "sharing %s\n", strings.Join(cfg.Expose, ", "))
	return nil
}

// runUnexpose stops sharing a session.
func runUnexpose(_ context.Context, env Env, args []string) error {
	fs := flagSet("unexpose", env.Stderr)
	if err := parse(fs, args); err != nil {
		return err
	}
	args = fs.Args()

	if len(args) == 0 {
		return errors.New("usage: claudio unexpose <session>")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	drop := map[string]bool{}
	for _, a := range args {
		drop[a] = true
	}
	kept := cfg.Expose[:0]
	for _, name := range cfg.Expose {
		if !drop[name] {
			kept = append(kept, name)
		}
	}
	cfg.Expose = kept
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "sharing %s\n", strings.Join(cfg.Expose, ", "))
	return nil
}

// runTrust sets the level this machine grants.
func runTrust(_ context.Context, env Env, args []string) error {
	fs := flagSet("trust", env.Stderr)
	forWorkspace := fs.Bool("workspace", false, "apply to everybody in the workspace")
	forSession := fs.String("session", "", "apply to one local session only")
	if err := parse(fs, args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if fs.NArg() == 0 {
		printTrust(env, cfg)
		return nil
	}

	var who, levelArg string
	switch {
	case *forWorkspace || *forSession != "":
		if fs.NArg() != 1 {
			return errors.New("usage: claudio trust --workspace <level>")
		}
		levelArg = fs.Arg(0)
	default:
		if fs.NArg() != 2 {
			return errors.New("usage: claudio trust <person> <level>")
		}
		who, levelArg = fs.Arg(0), fs.Arg(1)
	}

	level, err := trust.Parse(levelArg)
	if err != nil {
		return err
	}

	switch {
	case *forWorkspace:
		cfg.Trust.Workspace = level
	case *forSession != "":
		if cfg.Trust.Sessions == nil {
			cfg.Trust.Sessions = map[string]string{}
		}
		cfg.Trust.Sessions[*forSession] = level
	default:
		if cfg.Trust.People == nil {
			cfg.Trust.People = map[string]string{}
		}
		cfg.Trust.People[who] = level
	}

	if err := cfg.Save(); err != nil {
		return err
	}
	printTrust(env, cfg)
	return nil
}

func printTrust(env Env, cfg config.Config) {
	fmt.Fprintln(env.Stdout, "trust this machine grants:")
	fmt.Fprintf(env.Stdout, "  workspace  %s\n", cmpOr(cfg.Trust.Workspace, "no opinion"))
	for who, level := range cfg.Trust.People {
		fmt.Fprintf(env.Stdout, "  person     %-16s %s\n", who, level)
	}
	for name, level := range cfg.Trust.Sessions {
		fmt.Fprintf(env.Stdout, "  session    %-16s %s\n", name, level)
	}
	fmt.Fprintln(env.Stdout,
		"\nthe level actually used is the most restrictive of these and what the workspace proposes")
}

// runPolicy sets the promotion mode and the cap on native peers.
func runPolicy(_ context.Context, env Env, args []string) error {
	fs := flagSet("policy", env.Stderr)
	mode := fs.String("mode", "", "auto, manual, or off")
	// The default is negative rather than zero so that asking for zero can be
	// told apart from not asking. Zero is a real answer: it means run no extra
	// processes and reach everybody through the MCP tools.
	maxGhosts := fs.Int("max-ghosts", -1, "how many remote sessions get a native peer, zero for none")
	if err := parse(fs, args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if *mode != "" {
		switch *mode {
		case daemon.ModeAuto, daemon.ModeManual, daemon.ModeOff:
			cfg.Policy.Mode = *mode
		default:
			return fmt.Errorf("%q is not a mode, use auto, manual, or off", *mode)
		}
	}
	if *maxGhosts >= 0 {
		cfg.Policy = cfg.Policy.SetCap(*maxGhosts)
	}
	if *mode != "" || *maxGhosts >= 0 {
		if err := cfg.Save(); err != nil {
			return err
		}
	}

	// The workspace peer counts. It is not one of the ghosts the cap governs, it
	// is always there on top of them, and a figure that leaves it out understates
	// what somebody is agreeing to by exactly one process.
	processes := cfg.Policy.Cap()
	if cfg.Policy.Mode != daemon.ModeOff {
		processes++
	}
	fmt.Fprintf(env.Stdout, "mode        %s\n", cfg.Policy.Mode)
	fmt.Fprintf(env.Stdout, "max ghosts  %d, so %d processes and about %d MB of resident memory\n",
		cfg.Policy.Cap(), processes, processes*6+20)
	fmt.Fprintln(env.Stdout,
		"\nbeyond the cap the least recently used loses its process. Nothing becomes")
	if cfg.Policy.Mode == daemon.ModeOff {
		fmt.Fprintln(env.Stdout,
			"\noff means no processes at all, so nothing appears in your agent list and")
		fmt.Fprintln(env.Stdout,
			"everything goes through the MCP tools instead.")
	} else {
		fmt.Fprintln(env.Stdout,
			"unreachable, because the workspace peer still carries everybody.")
	}
	return nil
}

// describeTransport says whether the workspace can be reached, in a sentence
// somebody can act on.
//
// It exists because status used to answer the wrong question. With the relay
// stopped it went on printing the workspace and an empty roster, which looks
// exactly like a workspace where nobody is around, and only the connector log
// said otherwise.
func describeTransport(h daemon.TransportHealth) string {
	switch {
	case h.Connected:
		return "connected"
	case !h.EverConnected:
		reason := "it has not been reached yet"
		if h.Detail != "" {
			reason = h.Detail
		}
		return "never connected, so nothing in this workspace is reachable: " + reason
	default:
		since := ""
		if !h.Since.IsZero() {
			since = fmt.Sprintf(" since %s", h.Since.Format("15:04:05"))
		}
		reason := ""
		if h.Detail != "" {
			reason = ": " + h.Detail
		}
		return fmt.Sprintf("disconnected%s, retrying%s", since, reason)
	}
}

// runSessions lists what this machine can reach.
func runSessions(_ context.Context, env Env, _ []string) error {
	status, err := readStatus()
	if err != nil {
		return err
	}
	if len(status.Remote) == 0 {
		fmt.Fprintln(env.Stdout, "no remote sessions are reachable")
		return nil
	}
	for _, s := range status.Remote {
		fmt.Fprintf(env.Stdout, "%-28s %-10s %s/%s\n",
			s.ID, s.Status, s.Person, s.Session)
	}
	return nil
}

// runStatus explains what this machine is running.
//
// It carries a specific burden. Several processes named claudio in a task
// manager make people suspicious, so this has to say what each one is for.
func runStatus(_ context.Context, env Env, _ []string) error {
	status, err := readStatus()
	if err != nil {
		// Not an error to report. A connector that is not running is a perfectly
		// ordinary thing for status to find, and telling the person how to start
		// it is more useful than a non zero exit code.
		//
		fmt.Fprintln(env.Stdout, "the connector does not appear to be running")
		fmt.Fprintln(env.Stdout, "start it with \"claudio daemon\"")
		return nil //nolint:nilerr // the absence of a status file is a state, not a failure
	}

	fmt.Fprintf(env.Stdout, "workspace     %s\n", cmpOr(status.Workspace, "none"))
	fmt.Fprintf(env.Stdout, "platform      %s", status.Platform)
	if !status.Verified {
		fmt.Fprint(env.Stdout, " (not verified by this project)")
	}
	fmt.Fprintln(env.Stdout)
	if status.Degraded != "" {
		fmt.Fprintf(env.Stdout, "degraded      %s\n", status.Degraded)
	}
	fmt.Fprintf(env.Stdout, "relay         %s\n", describeTransport(status.Transport))
	fmt.Fprintf(env.Stdout, "mode          %s, up to %d native peers\n",
		status.Policy.Mode, status.Policy.Cap())
	fmt.Fprintf(env.Stdout, "session dirs  %s\n", strings.Join(status.SessionDirs, ", "))

	fmt.Fprintln(env.Stdout, "\nlocal Claude Code sessions:")
	if len(status.Local) == 0 {
		fmt.Fprintln(env.Stdout, "  none")
	}
	for _, s := range status.Local {
		fmt.Fprintf(env.Stdout, "  %-24s pid %-8d %-6s %s\n", s.Name, s.PID, s.Status, s.CWD)
	}

	fmt.Fprintln(env.Stdout, "\nprocesses this tool is running:")
	fmt.Fprintln(env.Stdout,
		"  each one holds a single endpoint so a remote session can appear in your agent list")
	if len(status.Peers) == 0 {
		fmt.Fprintln(env.Stdout, "  none")
	}
	for _, p := range status.Peers {
		state := "starting"
		if p.Ready {
			state = "ready"
		}
		line := fmt.Sprintf("  %-24s pid %-8d %s", p.Name, p.PID, state)
		if p.LastError != "" {
			line += "  last error: " + p.LastError
		}
		fmt.Fprintln(env.Stdout, line)
	}

	fmt.Fprintln(env.Stdout, "\nremote sessions:")
	if len(status.Remote) == 0 {
		fmt.Fprintln(env.Stdout, "  none reachable")
	}
	for _, s := range status.Remote {
		fmt.Fprintf(env.Stdout, "  %-24s %-8s %s/%s\n", s.ID, s.Status, s.Person, s.Session)
	}
	return nil
}

// runDoctor reports what this machine can and cannot do.
//
// It exists so that nobody has to guess whether something is unsupported, or
// merely unverified, or actually broken.
func runDoctor(_ context.Context, env Env, _ []string) error {
	platform := ccpeer.Platform()

	fmt.Fprintf(env.Stdout, "platform            %s\n", platform.GOOS())
	if platform.Verified() {
		fmt.Fprintln(env.Stdout, "protocol            verified against Claude Code 2.1.269")
	} else {
		fmt.Fprintln(env.Stdout, "protocol            NOT VERIFIED on this platform")
		fmt.Fprintln(env.Stdout,
			"                    native peers are not published here, and outbound messages")
		fmt.Fprintln(env.Stdout,
			"                    use the MCP transport instead. Delivery into a session works.")
	}
	fmt.Fprintf(env.Stdout, "auth line           %s\n",
		map[bool]string{true: "required on this platform", false: "optional on this platform"}[platform.RequiresAuthLine()])

	dirs, err := ccpeer.DefaultSessionsDir()
	if err == nil {
		fmt.Fprintf(env.Stdout, "sessions directory  %s\n", dirs)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	reg, err := ccpeer.NewRegistry(platform, cfg.SessionsDirs...)
	if err != nil {
		fmt.Fprintf(env.Stdout, "registry            unreadable: %v\n", err)
		return nil
	}
	fmt.Fprintf(env.Stdout, "resolved dirs       %s\n", strings.Join(reg.Dirs(), ", "))

	sessions, err := reg.List()
	if err != nil {
		fmt.Fprintf(env.Stdout, "local sessions      unreadable: %v\n", err)
	} else {
		fmt.Fprintf(env.Stdout, "local sessions      %d running\n", len(sessions))
		for _, s := range sessions {
			shared := " "
			if cfg.Exposes(s.Name) {
				shared = "shared"
			}
			fmt.Fprintf(env.Stdout, "                    %-24s %s\n", s.Name, shared)
		}
	}

	if runtime.GOOS == "windows" {
		fmt.Fprintln(env.Stdout,
			"\nnote                a session inside WSL2 and a native Windows session cannot")
		fmt.Fprintln(env.Stdout,
			"                    reach each other. That is a limit of Claude Code, not of this tool.")
	}

	if err := cfg.Ready(); err != nil {
		fmt.Fprintf(env.Stdout, "\nworkspace           %v\n", err)
	} else {
		fmt.Fprintf(env.Stdout, "\nworkspace           %s through %s\n", cfg.Workspace, cfg.Relay)
	}
	return nil
}

func loadIdentity() (*identity.Identity, error) {
	path, err := config.Path(config.FileIdentity)
	if err != nil {
		return nil, err
	}
	return identity.LoadOrCreate(path)
}

func readStatus() (daemon.Status, error) {
	path, err := config.Path(config.FileStatus)
	if err != nil {
		return daemon.Status{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return daemon.Status{}, fmt.Errorf("claudio: no status to read: %w", err)
	}
	var s daemon.Status
	if err := json.Unmarshal(raw, &s); err != nil {
		return daemon.Status{}, fmt.Errorf("claudio: the status file is unreadable: %w", err)
	}
	return s, nil
}

func storeOrDefault(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, config.FileWorkspace), nil
}

func cmpOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
