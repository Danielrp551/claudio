//go:build windows

package ccpeer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/Microsoft/go-winio"
)

// backslash avoids writing escaped path literals, which are easy to get wrong
// and hard to read in a file that is mostly about paths.
const backslash = string(rune(92))

// pipePrefix is the local named pipe namespace. Claude Code publishes its own
// inboxes under the session local namespace, which adds a LOCAL segment, but it
// accepts a plain path from a peer. Verified against 2.1.269: a ghost publishing
// \\.\pipe\cc-msg-<hex> appears in ListAgents and receives frames.
var pipePrefix = backslash + backslash + "." + backslash + "pipe" + backslash

type windowsEndpoint struct{}

func newPlatform() LocalEndpoint { return windowsEndpoint{} }

func (windowsEndpoint) GOOS() string { return "windows" }

func (windowsEndpoint) Verified() bool { return true }

func (windowsEndpoint) RequiresAuthLine() bool { return true }

func (w windowsEndpoint) NewInboxPath() (string, error) {
	return w.NewLocalPath("cc-msg")
}

func (windowsEndpoint) NewLocalPath(kind string) (string, error) {
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("ccpeer: generating an endpoint name: %w", err)
	}
	return pipePrefix + kind + "-" + hex.EncodeToString(suffix), nil
}

func (windowsEndpoint) Listen(path string) (net.Listener, error) {
	l, err := winio.ListenPipe(path, nil)
	if err != nil {
		return nil, fmt.Errorf("ccpeer: binding %s: %w", path, err)
	}
	return l, nil
}

func (windowsEndpoint) Dial(ctx context.Context, path string) (net.Conn, error) {
	c, err := winio.DialPipeContext(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("ccpeer: dialling %s: %w", path, err)
	}
	return c, nil
}

// ProcStart reads the creation time of a process as a FILETIME, which is the
// count of 100 nanosecond intervals since 1601. Claude Code writes this same
// encoding into Record.ProcStart on Windows.
func (windowsEndpoint) ProcStart(pid int) (string, error) {
	const queryLimitedInformation = 0x1000

	if pid <= 0 || int64(pid) > math.MaxUint32 {
		return "", fmt.Errorf("ccpeer: %d is not a usable process id", pid)
	}

	h, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		return "", fmt.Errorf("ccpeer: opening process %d: %w", pid, err)
	}
	defer syscall.CloseHandle(h)

	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return "", fmt.Errorf("ccpeer: reading times of process %d: %w", pid, err)
	}

	v := uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
	return strconv.FormatUint(v, 10), nil
}

func (windowsEndpoint) ProcessAlive(pid int) bool {
	const queryLimitedInformation = 0x1000

	if pid <= 0 || int64(pid) > math.MaxUint32 {
		return false
	}

	h, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259
	return code == stillActive
}

// PidDomain is "win32:" followed by the host name in lower case, which is what
// Claude Code writes. Windows has no process id namespaces, so the machine is
// the whole scope.
func (windowsEndpoint) PidDomain() (string, error) {
	host, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("ccpeer: reading the host name: %w", err)
	}
	return "win32:" + strings.ToLower(host), nil
}
