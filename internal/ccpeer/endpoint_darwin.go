//go:build darwin

package ccpeer

// darwinEndpoint supports the half of the protocol that is documented, and
// refuses the half that is not.
//
// Delivering into a session inbox works on macOS, because that interface is
// documented by Anthropic and needs nothing from us beyond the target's socket
// path and its key. So a macOS user can receive workspace messages natively.
//
// Publishing a native peer is different. It needs this project to write a
// session record with a start token and a process id domain in exactly the
// encoding Claude Code expects on macOS, and nobody has verified what that
// encoding is. Rather than guess and half work, those two operations return
// ErrUnverifiedPlatform and the daemon degrades to the MCP transport for
// outbound messages, saying so out loud.
//
// Fixing this is one probe on a Mac. See CONTRIBUTING.md.
type darwinEndpoint struct{ unixEndpoint }

func newPlatform() LocalEndpoint { return darwinEndpoint{} }

func (darwinEndpoint) GOOS() string { return "darwin" }

func (darwinEndpoint) Verified() bool { return false }

func (darwinEndpoint) ProcStart(int) (string, error) {
	return "", ErrUnverifiedPlatform
}

func (darwinEndpoint) PidDomain() (string, error) {
	return "", ErrUnverifiedPlatform
}
