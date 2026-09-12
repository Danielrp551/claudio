//go:build linux || darwin

package ccpeer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// unixEndpoint carries everything the Unix platforms share. Each platform
// embeds it and supplies its own process identity functions, which are the
// parts that genuinely differ.
type unixEndpoint struct{}

// RequiresAuthLine is false on Unix. Verified on Linux: a connection that sends
// a frame without an authentication line is not closed by the receiver, which
// matches the documented behaviour.
func (unixEndpoint) RequiresAuthLine() bool { return false }

// socketDir returns the directory this tool binds its own sockets in.
//
// It is deliberately not the directory Claude Code uses for its own sockets. We
// keep our files in our own place, with the same permissions Claude Code
// applies to its own, so nothing we create can be mistaken for, or cleaned up
// with, somebody else's state.
func socketDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = filepath.Join(os.TempDir(), fmt.Sprintf("claudio-%d", os.Getuid()))
	}
	dir := filepath.Join(base, "claudio", "socks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("ccpeer: creating %s: %w", dir, err)
	}
	// MkdirAll leaves an existing directory's permissions alone, so tighten it
	// explicitly. A world readable socket directory would defeat the point.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("ccpeer: tightening %s: %w", dir, err)
	}
	return dir, nil
}

func (u unixEndpoint) NewInboxPath() (string, error) {
	return u.NewLocalPath("inbox")
}

func (unixEndpoint) NewLocalPath(kind string) (string, error) {
	dir, err := socketDir()
	if err != nil {
		return "", err
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("ccpeer: generating an endpoint name: %w", err)
	}
	name := fmt.Sprintf("%s-%d-%s.sock", kind, os.Getpid(), hex.EncodeToString(suffix))
	return filepath.Join(dir, name), nil
}

func (unixEndpoint) Listen(path string) (net.Listener, error) {
	// A leftover socket from a process that died without cleaning up would make
	// Listen fail with "address already in use". Removing it is safe because the
	// name carries our own process id and a random suffix, so it can only be
	// ours.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("ccpeer: clearing %s: %w", path, err)
	}

	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("ccpeer: binding %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("ccpeer: tightening %s: %w", path, err)
	}
	return l, nil
}

func (unixEndpoint) Dial(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("ccpeer: dialling %s: %w", path, err)
	}
	return c, nil
}

// ProcessAlive uses signal zero, which performs the permission and existence
// checks without delivering anything.
func (unixEndpoint) ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists and belongs to somebody else. For our
	// purposes that is not alive, because a session we cannot signal is not a
	// session we own.
	return false
}
