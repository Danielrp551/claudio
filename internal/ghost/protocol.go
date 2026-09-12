package ghost

import "github.com/Danielrp551/claudio/internal/ccpeer"

// The control protocol between a daemon and one of its ghost children.
//
// It runs over the child's standard input and standard output as newline
// delimited JSON. That choice is not incidental: when the parent dies, the
// child's standard input closes, and closing standard input is the signal that
// makes it impossible for a ghost to outlive its daemon. See ADR-0004.
const (
	// OpInit tells a ghost which peer it represents and where to publish. It is
	// the first message the parent sends and it arrives exactly once.
	OpInit = "init"
	// OpStatus updates the status the ghost publishes in its record.
	OpStatus = "status"
	// OpShutdown asks for an orderly stop. A parent that dies without sending it
	// still stops the child, because standard input closes.
	OpShutdown = "shutdown"

	// OpReady reports that the endpoint is bound and the record is published.
	OpReady = "ready"
	// OpFrame carries a frame that arrived from a Claude Code session.
	OpFrame = "frame"
	// OpError reports a problem the parent should know about. A ghost reports
	// and keeps going where it can, and reports and exits where it cannot.
	OpError = "error"
)

// Command is a message from the daemon to a ghost.
type Command struct {
	Op string `json:"op"`

	// Name is the peer name for OpInit. It travels here rather than on the
	// command line so that a name, which can identify a person, does not end up
	// in the process table of a shared machine.
	Name string `json:"name,omitempty"`

	// SessionsDir is where the ghost publishes its record. The daemon resolves
	// it, so every child agrees on one directory even when several Claude Code
	// profiles reach it through different symbolic links.
	SessionsDir string `json:"sessionsDir,omitempty"`

	// Status is the value for OpStatus.
	Status string `json:"status,omitempty"`
}

// Event is a message from a ghost to its daemon.
type Event struct {
	Op string `json:"op"`

	// PID, RecordPath and Endpoint are set on OpReady. The daemon already knows
	// the process id from spawning the child, and compares it, because a
	// mismatch would mean the record it is tracking is not the one the child
	// wrote.
	PID        int    `json:"pid,omitempty"`
	RecordPath string `json:"recordPath,omitempty"`
	KeyPath    string `json:"keyPath,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`

	// Frame is set on OpFrame.
	Frame *ccpeer.Frame `json:"frame,omitempty"`

	// Message is set on OpError.
	Message string `json:"message,omitempty"`
}
