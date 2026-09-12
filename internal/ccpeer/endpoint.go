package ccpeer

import (
	"context"
	"net"
)

// LocalEndpoint hides the one part of this project that genuinely differs
// between operating systems: how a session inbox is named, bound, and dialled,
// and how the identity of a process is established.
//
// There is one implementation per platform, selected by build tags. Nothing
// outside this package branches on the operating system.
//
// Differences the implementations absorb, all verified against Claude Code
// 2.1.269 except where noted:
//
//	                     Windows                 Linux
//	endpoint             named pipe              $XDG_RUNTIME_DIR/cc-socks/<pid>.sock
//	auth line            mandatory               optional
//	start token          FILETIME since 1601     clock ticks since boot
//	pid domain           win32:<hostname>        linux:<machine-id>:<pid namespace>
//
// macOS is not verified. Its implementation reports Verified as false so the
// daemon can warn rather than pretend.
type LocalEndpoint interface {
	// GOOS names the platform this implementation serves.
	GOOS() string

	// Verified reports whether this implementation was checked against a real
	// Claude Code session by the maintainers. A false value is not a failure,
	// it is an invitation to be careful and to say so to the user.
	Verified() bool

	// NewLocalPath returns a fresh, unused local endpoint path for a process
	// owned by this user, creating the containing directory when the platform
	// needs one.
	//
	// The kind becomes part of the name. It exists so that an endpoint this tool
	// binds for its own purposes is not called something that looks like one of
	// Claude Code's, which would be misleading to anybody reading the list of
	// open pipes on a machine.
	NewLocalPath(kind string) (string, error)

	// NewInboxPath returns a path for an endpoint a Claude Code session will
	// connect to.
	NewInboxPath() (string, error)

	// Listen binds path so Claude Code sessions can connect to it.
	Listen(path string) (net.Listener, error)

	// Dial opens a connection to another session's inbox.
	Dial(ctx context.Context, path string) (net.Conn, error)

	// EndpointAlive reports whether an endpoint this process bound can still be
	// reached at path.
	//
	// It exists because on the Unix platforms the answer can be no while the
	// process is perfectly healthy. A socket lives in the filesystem, and
	// systemd removes the whole runtime directory when the user's last login
	// session ends, which happens to anybody who starts the connector over ssh
	// and then logs out. The kernel keeps the binding, so nothing fails and
	// nothing is logged, and the endpoint is simply unreachable for the rest of
	// its life.
	EndpointAlive(path string) bool

	// RequiresAuthLine reports whether this platform refuses a connection whose
	// first line is not a valid authentication line. It is true on Windows and
	// false elsewhere, and it is verified in both directions.
	RequiresAuthLine() bool

	// ProcStart returns the start token of a process, encoded the way this
	// platform writes Record.ProcStart. The value defeats process id reuse and
	// is not comparable across platforms.
	ProcStart(pid int) (string, error)

	// ProcessAlive reports whether a process id currently belongs to a live
	// process. It is the reliable presence signal, because a record can carry a
	// stale timestamp while its process is perfectly healthy.
	ProcessAlive(pid int) bool

	// PidDomain returns the value this platform writes into Record.PidDomain.
	PidDomain() (string, error)
}

// Platform returns the LocalEndpoint for the operating system this binary was
// built for.
func Platform() LocalEndpoint { return newPlatform() }
