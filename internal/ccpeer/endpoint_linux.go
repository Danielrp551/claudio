//go:build linux

package ccpeer

import (
	"fmt"
	"os"
	"strings"
)

// maxSocketPath is the longest socket path Linux can bind.
//
// syscall.RawSockaddrUnix carries Path [108]int8 here, and for a name that is a
// real path rather than an abstract one the standard library refuses a length
// that fills the array, because the terminating NUL needs the last byte.
const maxSocketPath = 107

type linuxEndpoint struct{ unixEndpoint }

func newPlatform() LocalEndpoint { return linuxEndpoint{} }

func (linuxEndpoint) GOOS() string { return "linux" }

func (linuxEndpoint) Verified() bool { return true }

// ProcStart returns field 22 of /proc/<pid>/stat, the start time of the process
// in clock ticks since boot. Verified against Claude Code 2.1.269: a session
// whose record carried 27273 had exactly 27273 in that field.
//
// This is not a timestamp and it is not comparable with the Windows encoding.
func (linuxEndpoint) ProcStart(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", fmt.Errorf("ccpeer: reading the stat of process %d: %w", pid, err)
	}

	// The second field is the executable name in parentheses and may itself
	// contain spaces and parentheses, so fields are counted from the last
	// closing parenthesis rather than from the start of the line.
	line := string(raw)
	closeIdx := strings.LastIndexByte(line, ')')
	if closeIdx < 0 || closeIdx+2 >= len(line) {
		return "", fmt.Errorf("ccpeer: malformed stat for process %d", pid)
	}

	fields := strings.Fields(line[closeIdx+2:])
	// After the comm field, field 3 of the file is the first element here, so
	// field 22 sits at index 19.
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex {
		return "", fmt.Errorf("ccpeer: stat of process %d has %d fields after comm, want more than %d",
			pid, len(fields), startTimeIndex)
	}
	return fields[startTimeIndex], nil
}

// PidDomain is "linux:" followed by the machine id and the process id namespace.
//
// The namespace is not decoration. A process id only means something inside its
// namespace, so including it is what stops two sessions in different containers
// from believing they verified each other.
//
// Verified against Claude Code 2.1.269, which produced exactly
// linux:<contents of /etc/machine-id>:<readlink of /proc/self/ns/pid>.
func (linuxEndpoint) PidDomain() (string, error) {
	id, err := machineID()
	if err != nil {
		return "", err
	}
	ns, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return "", fmt.Errorf("ccpeer: reading the pid namespace: %w", err)
	}
	return "linux:" + id + ":" + ns, nil
}

func machineID() (string, error) {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		raw, err := os.ReadFile(p)
		if err == nil {
			if id := strings.TrimSpace(string(raw)); id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("ccpeer: no machine id found")
}
