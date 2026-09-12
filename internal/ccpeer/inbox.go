package ccpeer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
)

// maxLineBytes bounds one protocol line. Claude Code caps a message at about a
// million characters, so this leaves room for the wrapper and for escaping while
// still refusing anything that could only be an attempt to exhaust memory.
const maxLineBytes = 4 << 20

// authLine is the first line a connection may send. It is mandatory on Windows
// and optional elsewhere.
type authLine struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

// Inbox accepts connections from Claude Code sessions and turns the frames they
// carry into a channel.
//
// Ownership is explicit: the Inbox creates the frame channel and is the only
// thing that closes it, and it closes it exactly once, after every connection
// goroutine has finished. A reader can therefore range over Frames and know that
// the range ending means the inbox is fully stopped.
type Inbox struct {
	ln     net.Listener
	frames chan Frame
	log    *slog.Logger

	// token is what this process published in its key file. A connection that
	// sends an authentication line is checked against it. A connection that
	// sends no authentication line is accepted, which is what a real Claude Code
	// session does when it cannot find our key file.
	tokenMu sync.Mutex
	token   string

	closeOnce sync.Once
	closeErr  error
	wg        sync.WaitGroup
}

// Listen binds path and starts accepting.
//
// The caller owns the returned Inbox and must Close it. Close is safe to call
// more than once, which matters because it runs on several shutdown paths.
func Listen(platform LocalEndpoint, path, token string, log *slog.Logger) (*Inbox, error) {
	if log == nil {
		log = slog.Default()
	}
	ln, err := platform.Listen(path)
	if err != nil {
		return nil, err
	}

	in := &Inbox{
		ln:     ln,
		frames: make(chan Frame, 16),
		log:    log,
		token:  token,
	}

	in.wg.Add(1)
	go in.accept()

	return in, nil
}

// Addr returns the path this inbox is bound to.
func (i *Inbox) Addr() string { return i.ln.Addr().String() }

// SetToken installs the token incoming authentication lines are checked against.
//
// It exists because of an ordering constraint. The endpoint must be bound before
// the record that points at it is published, so that no peer can read a record
// and fail to connect, but the token only exists once the record has been
// published. Binding first and installing the token a moment later closes the
// larger window at the cost of a smaller one, during which a connection is
// accepted without a check. That is the same treatment a real session gets when
// it cannot find our key file, so it is not a new exposure.
func (i *Inbox) SetToken(token string) {
	i.tokenMu.Lock()
	defer i.tokenMu.Unlock()
	i.token = token
}

func (i *Inbox) expectedToken() string {
	i.tokenMu.Lock()
	defer i.tokenMu.Unlock()
	return i.token
}

// Frames yields every frame that arrives. The channel closes when the inbox has
// stopped and every connection has drained.
func (i *Inbox) Frames() <-chan Frame { return i.frames }

// accept owns the listener. It exits when the listener is closed, which is the
// only way to stop it, and that is deliberate: there is exactly one shutdown
// path rather than a race between a context and a close.
func (i *Inbox) accept() {
	defer i.wg.Done()

	var conns sync.WaitGroup
	for {
		c, err := i.ln.Accept()
		if err != nil {
			// A closed listener is the normal way out, not a failure.
			if !errors.Is(err, net.ErrClosed) {
				i.log.Error("inbox stopped accepting", "error", err)
			}
			break
		}
		conns.Add(1)
		go func() {
			defer conns.Done()
			i.serve(c)
		}()
	}

	// Wait for the connections before closing the channel, so no goroutine can
	// ever send on a closed channel.
	conns.Wait()
	close(i.frames)
}

// serve reads one connection to completion.
//
// Claude Code opens a connection, may send an authentication line, sends one
// frame, and closes. It also opens connections that carry nothing at all, as
// part of the preflight it performs before delivering, so an empty connection is
// normal and must not be logged as a problem.
func (i *Inbox) serve(c net.Conn) {
	defer c.Close()

	r := bufio.NewReaderSize(c, 64<<10)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)

	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		if first {
			first = false
			ok, handled := i.checkAuth(line)
			if handled {
				if !ok {
					i.log.Warn("inbox refused a connection with a bad authentication line")
					return
				}
				continue
			}
		}

		f, err := DecodeFrame(line)
		if err != nil {
			i.log.Warn("inbox dropped a frame", "error", err)
			continue
		}
		i.frames <- f
	}

	if err := sc.Err(); err != nil {
		i.log.Debug("inbox connection ended", "error", err)
	}
}

// checkAuth inspects a first line. It reports whether the line was an
// authentication line at all, and whether it was acceptable.
//
// A line that is not an authentication line is not an error. On Unix the line is
// optional, and even on Windows a sender that cannot find our key file connects
// without one, which is the behaviour observed from a real session.
func (i *Inbox) checkAuth(line []byte) (ok, handled bool) {
	var a authLine
	if err := json.Unmarshal(line, &a); err != nil || a.Type != "auth" {
		return false, false
	}
	want := i.expectedToken()
	if want == "" {
		// We published no token, so there is nothing to check against.
		return true, true
	}
	return a.Token == want, true
}

// Close stops accepting and waits for every connection to finish. After it
// returns, the frame channel is closed and no goroutine of this inbox is left.
func (i *Inbox) Close() error {
	i.closeOnce.Do(func() {
		i.closeErr = i.ln.Close()
		i.wg.Wait()
	})
	if i.closeErr != nil && !errors.Is(i.closeErr, net.ErrClosed) {
		return fmt.Errorf("ccpeer: closing the inbox: %w", i.closeErr)
	}
	return nil
}
