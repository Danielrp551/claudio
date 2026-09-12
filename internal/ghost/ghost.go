package ghost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/danielrp551/claudio/internal/ccpeer"
)

// DefaultHeartbeat is how often a ghost refreshes its record.
//
// The record carries a timestamp that peers may use to judge staleness, and
// refreshing it also proves the file still exists. It is cheap: one small atomic
// write.
const DefaultHeartbeat = 5 * time.Second

// Config is everything a ghost needs. Every field has a working default except
// the streams, which the caller always owns.
type Config struct {
	// In carries commands from the daemon. When it reaches end of file, the
	// ghost shuts down, which is what makes a ghost unable to outlive its parent.
	In io.Reader
	// Out carries events to the daemon.
	Out io.Writer

	Platform  ccpeer.LocalEndpoint
	Logger    *slog.Logger
	Heartbeat time.Duration
}

// Run holds one native peer endpoint until the parent goes away.
//
// It returns nil for every orderly stop, which includes the parent closing
// standard input, an explicit shutdown command, and a cancelled context. It
// returns an error only when it could not do its job at all.
//
// Whatever happens, the record and the key this process published are removed
// before Run returns. That is the most important property in this package,
// because a record left behind is a peer Claude Code shows to the user with
// nobody answering.
func Run(ctx context.Context, cfg Config) error {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return err
	}
	out := &eventWriter{w: cfg.Out}

	init, err := readInit(cfg.In)
	if err != nil {
		return err
	}

	reg, err := ccpeer.NewRegistry(cfg.Platform, init.SessionsDir)
	if err != nil {
		return err
	}

	path, err := cfg.Platform.NewInboxPath()
	if err != nil {
		return err
	}

	// Bind before publishing, so no peer can read a record and then fail to
	// connect to the endpoint it names.
	inbox, err := ccpeer.Listen(cfg.Platform, path, "", cfg.Logger)
	if err != nil {
		return err
	}
	defer func() { _ = inbox.Close() }()

	pub, err := reg.Publish(init.Name, path)
	if err != nil {
		return err
	}

	// Withdrawal runs exactly once, whichever path leads out of this function.
	// The orderly path below calls it explicitly, in its proper place. This
	// deferred call is the safety net for an early return.
	var withdrawOnce sync.Once
	withdraw := func() {
		withdrawOnce.Do(func() {
			if err := reg.Withdraw(pub); err != nil {
				cfg.Logger.Error("ghost could not remove its record",
					"error", err, "path", pub.RecordPath)
				return
			}
			cfg.Logger.Info("ghost withdrew its record", "path", pub.RecordPath)
		})
	}
	defer withdraw()

	inbox.SetToken(pub.Token)

	if err := out.send(Event{
		Op:         OpReady,
		PID:        os.Getpid(),
		RecordPath: pub.RecordPath,
		KeyPath:    pub.KeyPath,
		Endpoint:   path,
	}); err != nil {
		return err
	}
	cfg.Logger.Info("ghost is up", "name", init.Name, "pid", os.Getpid(), "endpoint", path)

	serve(ctx, cfg, reg, inbox, pub, out)

	// The order below is the point of this function, and it is not negotiable.
	//
	// Stop accepting first, so no new frame can arrive. Then wait for the
	// heartbeat to have finished, because a heartbeat that runs after the record
	// is removed writes it back and leaves an orphan. Only then withdraw.
	//
	// That failure is not hypothetical. It happened in a probe, and this
	// ordering, with the test that covers it, is the answer to it. serve returns
	// only once both of those are true.
	_ = inbox.Close()
	withdraw()
	return nil
}

func (c Config) withDefaults() (Config, error) {
	if c.In == nil || c.Out == nil {
		return c, errors.New("ghost: In and Out are required")
	}
	if c.Platform == nil {
		c.Platform = ccpeer.Platform()
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = DefaultHeartbeat
	}
	return c, nil
}

// serve runs the concurrent jobs of a live ghost and returns only when all of
// them have stopped. Every goroutine it starts is waited for, so when it returns
// the caller can remove the record knowing nothing will write it back.
func serve(
	ctx context.Context,
	cfg Config,
	reg *ccpeer.Registry,
	inbox *ccpeer.Inbox,
	pub ccpeer.Publication,
	out *eventWriter,
) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := &status{value: ccpeer.StatusIdle}

	var forwarder sync.WaitGroup
	forwarder.Add(1)
	go func() {
		defer forwarder.Done()
		// Exits when the inbox closes its channel, which happens only after
		// every connection has drained.
		for f := range inbox.Frames() {
			frame := f
			if err := out.send(Event{Op: OpFrame, Frame: &frame}); err != nil {
				cfg.Logger.Error("ghost could not forward a frame", "error", err)
				return
			}
		}
	}()

	var heartbeat sync.WaitGroup
	heartbeat.Add(1)
	go func() {
		defer heartbeat.Done()
		runHeartbeat(ctx, cfg, reg, pub, state)
	}()

	// The control loop ends when the parent goes away, which is the normal end
	// of a ghost's life.
	commands := make(chan error, 1)
	go func() {
		commands <- readCommands(cfg.In, func(c Command) {
			switch c.Op {
			case OpStatus:
				state.set(c.Status)
			case OpShutdown:
				cancel()
			default:
				cfg.Logger.Warn("ghost ignored an unknown command", "op", c.Op)
			}
		})
	}()

	select {
	case <-ctx.Done():
	case err := <-commands:
		if err != nil && !errors.Is(err, io.EOF) {
			cfg.Logger.Warn("ghost stopped reading commands", "error", err)
		}
		cancel()
	}

	// Stop accepting, then drain the forwarder, then be certain the heartbeat
	// has finished. Only after this does the caller touch the record.
	_ = inbox.Close()
	forwarder.Wait()
	heartbeat.Wait()
}

func runHeartbeat(
	ctx context.Context,
	cfg Config,
	reg *ccpeer.Registry,
	pub ccpeer.Publication,
	state *status,
) {
	// One timer, reset each round, rather than a fresh timer per iteration.
	t := time.NewTimer(cfg.Heartbeat)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := reg.Update(pub, state.get()); err != nil {
				cfg.Logger.Warn("ghost could not refresh its record", "error", err)
			}
			t.Reset(cfg.Heartbeat)
		}
	}
}

// status is the small piece of shared state a ghost has. A mutex rather than a
// channel because it protects a field rather than transferring ownership.
type status struct {
	mu    sync.Mutex
	value string
}

func (s *status) set(v string) {
	if v == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = v
}

func (s *status) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

// readInit waits for the one command that has to arrive before anything else.
func readInit(r io.Reader) (Command, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 8<<10), 1<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var c Command
		if err := json.Unmarshal(line, &c); err != nil {
			return Command{}, fmt.Errorf("ghost: unreadable command: %w", err)
		}
		if c.Op != OpInit {
			return Command{}, fmt.Errorf("ghost: first command is %q, want %q", c.Op, OpInit)
		}
		if c.Name == "" {
			return Command{}, errors.New("ghost: init carries no name")
		}
		return c, nil
	}
	if err := sc.Err(); err != nil {
		return Command{}, fmt.Errorf("ghost: reading the first command: %w", err)
	}
	return Command{}, errors.New("ghost: the parent closed the control stream before sending init")
}

// readCommands runs until the stream ends. The callback runs on this goroutine,
// so it must not block for long.
func readCommands(r io.Reader, handle func(Command)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 8<<10), 1<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var c Command
		if err := json.Unmarshal(line, &c); err != nil {
			// A malformed command is not worth dying over, and dying would take
			// a healthy peer down with it.
			continue
		}
		handle(c)
	}
	return sc.Err()
}

// eventWriter serialises writes, because the forwarder and the startup path both
// send events.
type eventWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (e *eventWriter) send(ev Event) error {
	blob, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("ghost: encoding an event: %w", err)
	}
	blob = append(blob, '\n')

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.w.Write(blob); err != nil {
		return fmt.Errorf("ghost: writing an event: %w", err)
	}
	return nil
}
