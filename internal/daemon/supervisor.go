package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/Danielrp551/claudio/internal/ccpeer"
	"github.com/Danielrp551/claudio/internal/ghost"
)

const (
	// minBackoff and maxBackoff bound how fast a failing ghost is restarted. A
	// ghost that cannot bind its endpoint would otherwise spin.
	minBackoff = 200 * time.Millisecond
	maxBackoff = 30 * time.Second

	// healthyRun is how long a ghost has to live before its restart backoff is
	// considered spent. Anything shorter is treated as part of the same failure.
	healthyRun = 30 * time.Second

	// maxConsecutiveFailures is how many times a ghost is restarted before the
	// supervisor gives up on that peer and says so. Retrying for ever hides a
	// problem the user could fix.
	maxConsecutiveFailures = 8

	// readyTimeout bounds how long a freshly started ghost has to publish its
	// record and report back.
	readyTimeout = 15 * time.Second
)

// Inbound is a frame that arrived at one of this machine's native peers,
// together with the peer it arrived at.
type Inbound struct {
	// Peer is the name of the ghost that received it, which identifies the
	// remote session the local Claude was writing to.
	Peer string
	// Frame is what the local Claude Code session sent.
	Frame ccpeer.Frame
}

// supervisor keeps one ghost process alive per native peer.
//
// Every goroutine it starts is owned by it and waited for in Close. The frame
// channel is created here and closed here, once, after every child has stopped,
// so a reader can range over it and trust that the range ending means the
// supervisor is fully stopped.
type supervisor struct {
	executable  string
	args        []string
	sessionsDir string
	platform    ccpeer.LocalEndpoint
	index       *orphanIndex
	log         *slog.Logger

	frames chan Inbound

	mu     sync.Mutex
	wanted map[string]*peerState
	closed bool

	wg sync.WaitGroup

	ctx    context.Context
	cancel context.CancelFunc
}

// peerState is what the supervisor knows about one native peer.
type peerState struct {
	name string

	mu       sync.Mutex
	pid      int
	endpoint string
	ready    bool
	failures int
	lastErr  error

	stop    context.CancelFunc
	stopped chan struct{}
}

func (p *peerState) snapshot() (pid int, endpoint string, ready bool, failures int, lastErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pid, p.endpoint, p.ready, p.failures, p.lastErr
}

func newSupervisor(
	ctx context.Context,
	executable string,
	args []string,
	sessionsDir string,
	platform ccpeer.LocalEndpoint,
	index *orphanIndex,
	log *slog.Logger,
) *supervisor {
	ctx, cancel := context.WithCancel(ctx)
	return &supervisor{
		executable:  executable,
		args:        args,
		sessionsDir: sessionsDir,
		platform:    platform,
		index:       index,
		log:         log,
		frames:      make(chan Inbound, 64),
		wanted:      map[string]*peerState{},
		ctx:         ctx,
		cancel:      cancel,
	}
}

// Frames yields every frame that arrives at any native peer. It closes when the
// supervisor has stopped and every child has been reaped.
func (s *supervisor) Frames() <-chan Inbound { return s.frames }

// Ensure makes sure a native peer with this name exists, starting it if needed.
// Calling it for a peer that already runs does nothing.
func (s *supervisor) Ensure(name string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("daemon: the supervisor is closed")
	}
	if _, exists := s.wanted[name]; exists {
		s.mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(s.ctx)
	st := &peerState{name: name, stop: cancel, stopped: make(chan struct{})}
	s.wanted[name] = st
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(st.stopped)
		s.keepAlive(ctx, st)
	}()

	return nil
}

// Release stops a native peer and waits for its process to be gone, so the
// caller can rely on the record having been removed when it returns.
func (s *supervisor) Release(name string) {
	s.mu.Lock()
	st, exists := s.wanted[name]
	if exists {
		delete(s.wanted, name)
	}
	s.mu.Unlock()

	if !exists {
		return
	}
	st.stop()
	<-st.stopped
}

// Peers reports what the supervisor is running, for diagnostics.
func (s *supervisor) Peers() []PeerStatus {
	s.mu.Lock()
	states := make([]*peerState, 0, len(s.wanted))
	for _, st := range s.wanted {
		states = append(states, st)
	}
	s.mu.Unlock()

	out := make([]PeerStatus, 0, len(states))
	for _, st := range states {
		pid, endpoint, ready, failures, lastErr := st.snapshot()
		ps := PeerStatus{
			Name:     st.name,
			PID:      pid,
			Endpoint: endpoint,
			Ready:    ready,
			Failures: failures,
		}
		if lastErr != nil {
			ps.LastError = lastErr.Error()
		}
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PeerStatus is what claudio status shows about one native peer. Several
// processes named claudio in a task manager make users suspicious, so being able
// to say what each one is doing is part of the product.
type PeerStatus struct {
	Name      string `json:"name"`
	PID       int    `json:"pid"`
	Endpoint  string `json:"endpoint"`
	Ready     bool   `json:"ready"`
	Failures  int    `json:"failures"`
	LastError string `json:"lastError,omitempty"`
}

// keepAlive runs one ghost, restarting it while it is still wanted.
func (s *supervisor) keepAlive(ctx context.Context, st *peerState) {
	backoff := minBackoff

	for {
		started := time.Now()
		err := s.runOnce(ctx, st)
		lived := time.Since(started)

		if ctx.Err() != nil {
			return
		}

		st.mu.Lock()
		st.ready = false
		st.pid = 0
		if lived >= healthyRun {
			st.failures = 0
			backoff = minBackoff
		} else {
			st.failures++
		}
		st.lastErr = err
		failures := st.failures
		st.mu.Unlock()

		if err != nil {
			s.log.Warn("a native peer stopped", "peer", st.name, "error", err, "lived", lived)
		} else {
			s.log.Info("a native peer exited", "peer", st.name, "lived", lived)
		}

		if failures >= maxConsecutiveFailures {
			s.log.Error("giving up on a native peer after repeated failures",
				"peer", st.name, "failures", failures, "lastError", err)
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// runOnce starts one ghost and returns when it has exited.
func (s *supervisor) runOnce(ctx context.Context, st *peerState) error {
	// The executable is this program's own path, resolved at startup, and the
	// arguments are a fixed list this package controls. Neither comes from a
	// message or from anything a peer can influence.
	//
	//nolint:gosec // the command is this binary, spawning itself as a ghost
	cmd := exec.CommandContext(ctx, s.executable, s.args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("daemon: opening the control stream of %s: %w", st.name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("daemon: opening the event stream of %s: %w", st.name, err)
	}

	// Closing standard input is how a ghost is told to stop, so the process must
	// not be killed out from under it while it is cleaning up. Cancel closes the
	// pipe through the deferred call below instead.
	cmd.Cancel = func() error { return stdin.Close() }
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("daemon: starting a ghost for %s: %w", st.name, err)
	}

	pid := cmd.Process.Pid

	// Track before the child is told to publish anything. If it publishes and
	// dies before reporting back, the next sweep still knows what to remove.
	if err := s.index.Track(pid, st.name, s.sessionsDir); err != nil {
		s.log.Error("could not track a ghost", "peer", st.name, "pid", pid, "error", err)
	}

	st.mu.Lock()
	st.pid = pid
	st.mu.Unlock()

	init, err := json.Marshal(ghost.Command{
		Op:          ghost.OpInit,
		Name:        st.name,
		SessionsDir: s.sessionsDir,
	})
	if err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return fmt.Errorf("daemon: encoding init for %s: %w", st.name, err)
	}
	if _, err := stdin.Write(append(init, '\n')); err != nil {
		_ = stdin.Close()
		_ = cmd.Wait()
		return fmt.Errorf("daemon: sending init to %s: %w", st.name, err)
	}

	events := make(chan ghost.Event, 16)
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		defer close(events)
		readEvents(stdout, events)
	}()

	// Consume events until the child's stream ends.
	ready := time.NewTimer(readyTimeout)
	defer ready.Stop()

	sawReady := false
	for {
		select {
		case ev, open := <-events:
			if !open {
				reader.Wait()
				_ = stdin.Close()
				waitErr := cmd.Wait()
				_ = s.index.Forget(pid)
				if !sawReady {
					return fmt.Errorf("daemon: the ghost for %s exited before it was ready: %w",
						st.name, waitErr)
				}
				return waitErr
			}
			switch ev.Op {
			case ghost.OpReady:
				sawReady = true
				ready.Stop()
				st.mu.Lock()
				st.ready = true
				st.endpoint = ev.Endpoint
				st.mu.Unlock()
				s.log.Info("native peer is up",
					"peer", st.name, "pid", ev.PID, "endpoint", ev.Endpoint)
			case ghost.OpFrame:
				if ev.Frame == nil {
					continue
				}
				select {
				case s.frames <- Inbound{Peer: st.name, Frame: *ev.Frame}:
				case <-ctx.Done():
				}
			case ghost.OpError:
				s.log.Warn("a native peer reported a problem",
					"peer", st.name, "message", ev.Message)
			}

		case <-ready.C:
			if !sawReady {
				_ = stdin.Close()
				reader.Wait()
				_ = cmd.Wait()
				_ = s.index.Forget(pid)
				return fmt.Errorf("daemon: the ghost for %s did not become ready in %s",
					st.name, readyTimeout)
			}

		case <-ctx.Done():
			// Close the control stream and let the child clean up. Wait picks up
			// the exit, and WaitDelay stops a stuck child from hanging shutdown.
			_ = stdin.Close()
			reader.Wait()
			err := cmd.Wait()
			_ = s.index.Forget(pid)
			if err != nil && !errors.Is(err, context.Canceled) {
				s.log.Debug("a native peer exited after being asked to stop",
					"peer", st.name, "error", err)
			}
			return nil
		}
	}
}

func readEvents(r io.Reader, out chan<- ghost.Event) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev ghost.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		out <- ev
	}
}

// Close stops every child and waits for all of them. After it returns, no
// goroutine of this supervisor is left and the frame channel is closed.
func (s *supervisor) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	s.cancel()
	s.wg.Wait()
	close(s.frames)
	return nil
}
