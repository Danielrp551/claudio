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
	"strings"
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

// candidate is one place this tool is willing to keep its own sockets. The base
// is a directory the system already provides, and rel is the single element
// created under it.
type candidate struct {
	base string
	rel  string
}

// socketDirs lists the directories this tool binds its own sockets in, best
// first.
//
// They are deliberately not the directory Claude Code uses for its own sockets.
// We keep our files in our own place, with the same permissions Claude Code
// applies to its own, so nothing we create can be mistaken for, or cleaned up
// with, somebody else's state.
//
// There is more than one candidate because a Unix socket address is not a path
// of any length. It has to fit in sun_path, and macOS spends 46 bytes of a 104
// byte budget on the per user temporary directory alone. Rather than bind a
// path that is one directory name away from failing, the list ends in a short
// fallback. Claude Code itself falls back the same way, to /tmp/cc-socks-<uid>.
func socketDirs() []candidate {
	var dirs []candidate

	// XDG_RUNTIME_DIR is already private to this user and short, so it is the
	// first choice wherever the system provides one.
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		dirs = append(dirs, candidate{base: base, rel: "claudio"})
	}

	// The user id is in the name because the temporary directory can be shared,
	// and two users must never land on the same one.
	owned := fmt.Sprintf("claudio-%d", os.Getuid())
	tmp := os.TempDir()
	dirs = append(dirs, candidate{base: tmp, rel: owned})
	if tmp != "/tmp" {
		dirs = append(dirs, candidate{base: "/tmp", rel: owned})
	}
	return dirs
}

func (u unixEndpoint) NewInboxPath() (string, error) {
	return u.NewLocalPath("inbox")
}

// NewLocalPath returns a path that is unused, private to this user, and short
// enough to bind.
//
// The length check is the point. Binding a path that does not fit sun_path
// fails with EINVAL, which arrives as "invalid argument" and says nothing about
// what is actually wrong. Checking first means the error names the limit, and
// means the short fallback gets used instead of the failure happening at all.
func (unixEndpoint) NewLocalPath(kind string) (string, error) {
	name, err := socketName(kind)
	if err != nil {
		return "", err
	}

	var refused []string
	for _, c := range socketDirs() {
		dir := filepath.Join(c.base, c.rel)
		path := filepath.Join(dir, name)

		if len(path) > maxSocketPath {
			refused = append(refused, fmt.Sprintf("%s is %d bytes and a socket path here holds %d",
				path, len(path), maxSocketPath))
			continue
		}
		if err := ensureOwnedDir(c.base, c.rel); err != nil {
			refused = append(refused, err.Error())
			continue
		}
		return path, nil
	}
	return "", fmt.Errorf("ccpeer: no usable socket directory: %s", strings.Join(refused, "; "))
}

func socketName(kind string) (string, error) {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("ccpeer: generating an endpoint name: %w", err)
	}
	return fmt.Sprintf("%s-%d-%s.sock", kind, os.Getpid(), hex.EncodeToString(suffix)), nil
}

// ensureOwnedDir creates base/rel if it is missing and refuses it if it is not
// a directory this user owns alone.
//
// The check is not ceremony. One of the candidates lives under a world writable
// directory, so somebody else on the machine can get there first: as a
// directory of their own, which would put our sockets where they can replace
// them, or as a symbolic link, which would point the tightening chmod at a
// target they chose. Both are refused here.
func ensureOwnedDir(base, rel string) error {
	dir := filepath.Join(base, rel)

	// Mkdir rather than MkdirAll, because the whole point is to look at what is
	// there rather than to create a chain of directories unexamined. The base
	// is a directory the system provides and is taken as given.
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	// Lstat rather than Stat: a symbolic link here is exactly what an attack on
	// a shared directory looks like, and Stat would follow it.
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspecting %s: %w", dir, err)
	}
	if !info.Mode().IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s cannot be checked for ownership", dir)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to user %d rather than to this one", dir, stat.Uid)
	}

	// Mkdir leaves an existing directory's permissions alone, so tighten it
	// explicitly. A socket directory anybody can write to would defeat the
	// point. Tightening rather than refusing is right here, because by now the
	// directory is known to be ours.
	if info.Mode().Perm()&0o077 != 0 {
		//nolint:gosec // G302 is about files. A directory needs the execute bit
		// to be usable at all, so 0700 is already the tightest mode there is.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("tightening %s: %w", dir, err)
		}
	}
	return nil
}

func (unixEndpoint) Listen(path string) (net.Listener, error) {
	// A leftover socket from a process that died without cleaning up would make
	// Listen fail with "address already in use". Removing it is safe because the
	// name carries our own process id and a random suffix, so it can only be
	// ours.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("ccpeer: clearing %s: %w", path, err)
	}

	// ListenConfig rather than net.Listen, which is the same call with a
	// context the standard library supplies itself. Binding a Unix socket does
	// not block on anything cancellable, so Background is the honest argument.
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "unix", path)
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
