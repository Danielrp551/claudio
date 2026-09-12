package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// The control protocol between the connector and the other things this binary
// runs, as newline delimited JSON over a local endpoint.
//
// It exists because the MCP server and the command line are separate processes
// from the connector, and they need to ask it things and tell it things. The
// endpoint is local and owned by this user, exactly like the ones Claude Code
// binds, so no port is opened and nothing crosses the network.
const (
	ControlStatus  = "status"
	ControlSend    = "send"
	ControlPromote = "promote"
	ControlDemote  = "demote"
)

// ControlRequest is one call.
type ControlRequest struct {
	Op string `json:"op"`

	// Send.
	To          string `json:"to,omitempty"`
	Text        string `json:"text,omitempty"`
	FromSession string `json:"fromSession,omitempty"`
	FromMode    string `json:"fromMode,omitempty"`

	// Promote and demote.
	Session string `json:"session,omitempty"`
}

// ControlResponse is one answer.
type ControlResponse struct {
	OK     bool    `json:"ok"`
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
	MsgID  string  `json:"msgId,omitempty"`
}

// AddressFile is where the connector writes the path of its control endpoint, so
// another process can find it without guessing.
const AddressFile = "control.addr"

// endpointCheckInterval is how often the control endpoint is checked for still
// being there. It is frequent enough that a person does not notice the gap and
// rare enough to be free.
const endpointCheckInterval = 5 * time.Second

// serveControl keeps a control endpoint available until the context is
// cancelled.
//
// It is a loop rather than a single bind because on the Unix platforms the
// socket can be removed while this process is healthy: the runtime directory
// belongs to a login session, and systemd cleans it when the last one ends.
// Nothing fails when that happens, which is the problem. The listener keeps
// listening at an address nothing can reach, so every later claudio command
// reports that the connector is not running while it is running perfectly well.
func (d *Daemon) serveControl(ctx context.Context, addressPath string) error {
	defer os.Remove(addressPath)

	for {
		gone, err := d.serveControlOnce(ctx, addressPath)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if gone {
			d.log.Warn("the control endpoint was removed from underneath the connector, binding another")
			continue
		}
		return nil
	}
}

// serveControlOnce binds one endpoint and serves it. It reports whether it
// stopped because the endpoint disappeared, which is the one reason worth
// retrying.
func (d *Daemon) serveControlOnce(ctx context.Context, addressPath string) (bool, error) {
	path, err := d.platform.NewLocalPath("claudio-ctl")
	if err != nil {
		return false, err
	}
	ln, err := d.platform.Listen(path)
	if err != nil {
		return false, err
	}

	if err := os.WriteFile(addressPath, []byte(path), 0o600); err != nil {
		_ = ln.Close()
		return false, fmt.Errorf("daemon: publishing the control address: %w", err)
	}

	d.log.Info("control endpoint ready", "path", path)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// vanished is written by the watchdog and read after Accept fails, so the
	// loop above can tell a missing endpoint from a real error.
	vanished := make(chan struct{})

	var watchdog sync.WaitGroup
	watchdog.Add(1)
	go func() {
		defer watchdog.Done()
		d.watchEndpoint(ctx, path, vanished, cancel)
	}()

	var conns sync.WaitGroup
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			conns.Wait()
			cancel()
			watchdog.Wait()

			select {
			case <-vanished:
				return true, nil
			default:
			}
			if ctx.Err() != nil {
				return false, nil
			}
			return false, fmt.Errorf("daemon: the control endpoint stopped accepting: %w", err)
		}
		conns.Add(1)
		go func() {
			defer conns.Done()
			d.serveControlConn(ctx, conn)
		}()
	}
}

// watchEndpoint reports that path stopped existing and stops the server, which
// is what makes Accept return so the endpoint can be bound again.
//
// The order matters. The channel is closed before the context is cancelled, so
// that by the time Accept fails the reason is already recorded and cannot be
// mistaken for an ordinary shutdown.
func (d *Daemon) watchEndpoint(ctx context.Context, path string, vanished chan<- struct{}, stop func()) {
	t := time.NewTicker(endpointCheckInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !d.platform.EndpointAlive(path) {
				close(vanished)
				stop()
				return
			}
		}
	}
}

func (d *Daemon) serveControlConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)

	for sc.Scan() {
		var req ControlRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			writeControl(conn, ControlResponse{Error: "that request could not be read"})
			return
		}
		writeControl(conn, d.handleControl(ctx, req))
	}
}

func (d *Daemon) handleControl(ctx context.Context, req ControlRequest) ControlResponse {
	switch req.Op {
	case ControlStatus:
		status := d.Snapshot()
		return ControlResponse{OK: true, Status: &status}

	case ControlSend:
		if req.To == "" || req.Text == "" {
			return ControlResponse{Error: "a send needs a destination and some text"}
		}
		msg := Outbound{
			To:          req.To,
			Text:        req.Text,
			FromSession: req.FromSession,
			FromMode:    cmpOr(req.FromMode, "prompting"),
			MsgID:       newMessageID(),
		}
		if err := d.opts.Transport.Send(ctx, msg); err != nil {
			return ControlResponse{Error: err.Error()}
		}
		d.mu.Lock()
		d.lastUsed[req.To] = time.Now()
		d.mu.Unlock()

		// The transport accepted it. That is not the same as anybody reading it,
		// and the caller is told exactly that. See ADR-0007.
		return ControlResponse{OK: true, MsgID: msg.MsgID}

	case ControlPromote:
		if req.Session == "" {
			return ControlResponse{Error: "promote needs a session"}
		}
		d.subscribe(req.Session, true)
		return ControlResponse{OK: true}

	case ControlDemote:
		if req.Session == "" {
			return ControlResponse{Error: "demote needs a session"}
		}
		d.subscribe(req.Session, false)
		return ControlResponse{OK: true}

	default:
		return ControlResponse{Error: fmt.Sprintf("unknown operation %q", req.Op)}
	}
}

// subscribe adds or removes a remote session from the set this machine promotes
// to a native peer. The next poll reconciles the processes.
func (d *Daemon) subscribe(session string, want bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	kept := d.opts.Policy.Subscribed[:0]
	for _, s := range d.opts.Policy.Subscribed {
		if s != session {
			kept = append(kept, s)
		}
	}
	if want {
		kept = append(kept, session)
	}
	d.opts.Policy.Subscribed = kept

	// Promoting something explicitly only has an effect in manual mode, so asking
	// for it switches the machine into that mode rather than silently doing
	// nothing.
	if want && d.opts.Policy.Mode == ModeOff {
		d.opts.Policy.Mode = ModeManual
	}
}

func writeControl(conn net.Conn, resp ControlResponse) {
	blob, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(blob, '\n'))
}

// ControlClient talks to a running connector.
type ControlClient struct {
	platform interface {
		Dial(ctx context.Context, path string) (net.Conn, error)
	}
	path string
}

// DialControl connects to the connector whose address is written at addressPath.
func DialControl(addressPath string, platform interface {
	Dial(ctx context.Context, path string) (net.Conn, error)
}) (*ControlClient, error) {
	raw, err := os.ReadFile(addressPath)
	if err != nil {
		return nil, fmt.Errorf("daemon: the connector does not appear to be running: %w", err)
	}
	return &ControlClient{platform: platform, path: string(raw)}, nil
}

// Call makes one request and returns the answer.
func (c *ControlClient) Call(ctx context.Context, req ControlRequest) (ControlResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	conn, err := c.platform.Dial(ctx, c.path)
	if err != nil {
		return ControlResponse{}, fmt.Errorf("daemon: reaching the connector: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	blob, err := json.Marshal(req)
	if err != nil {
		return ControlResponse{}, fmt.Errorf("daemon: encoding a request: %w", err)
	}
	if _, err := conn.Write(append(blob, '\n')); err != nil {
		return ControlResponse{}, fmt.Errorf("daemon: sending a request: %w", err)
	}

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return ControlResponse{}, fmt.Errorf("daemon: reading the answer: %w", err)
		}
		return ControlResponse{}, errors.New("daemon: the connector closed without answering")
	}

	var resp ControlResponse
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		return ControlResponse{}, fmt.Errorf("daemon: the answer could not be read: %w", err)
	}
	if !resp.OK && resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}
