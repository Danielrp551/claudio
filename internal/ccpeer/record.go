package ccpeer

// PeerProtocol is the registry protocol version this build understands. It is
// the second of the two numbers used to decide whether the native path is safe
// to take, the first being FrameVersion. See ADR-0008.
const PeerProtocol = 1

// PublishedVersion is the Claude Code version this build writes into the records
// it publishes.
//
// A peer reads this field to reason about what the other side can do, so the
// honest value is the version whose protocol we implement and verified against,
// rather than a version of our own that would mean nothing to a reader.
const PublishedVersion = "2.1.269"

// Session status values, as they appear in a record.
const (
	StatusIdle = "idle"
	StatusBusy = "busy"
)

// Record is the on disk session record, stored as <pid>.json in the sessions
// directory of a Claude Code configuration directory.
//
// The schema below was captured from live sessions on Windows 11 and on Linux,
// both running Claude Code 2.1.269. Fields are documented where their meaning is
// not obvious or where it differs between platforms.
type Record struct {
	// PID is the process id of the session. The file name must carry this same
	// number, and that number is what Claude Code treats as the identity.
	PID int `json:"pid"`

	// SessionID is not a stable key. A /clear mints a new one without restarting
	// the process, so a caller holding an older value would refuse to deliver to
	// a session that is alive and reachable.
	SessionID string `json:"sessionId"`

	CWD       string `json:"cwd"`
	StartedAt int64  `json:"startedAt"`

	// ProcStart is the process start token, which defeats process id reuse. Its
	// meaning is platform specific and the two are not comparable:
	//
	//   Windows: a FILETIME, meaning 100 nanosecond intervals since 1601.
	//   Linux:   clock ticks since boot, field 22 of /proc/<pid>/stat.
	//
	// Nothing outside this package should interpret this value.
	ProcStart string `json:"procStart"`

	Version      string   `json:"version"`
	PeerProtocol int      `json:"peerProtocol"`
	PeerFeatures []string `json:"peerFeatures"`
	Kind         string   `json:"kind"`
	Entrypoint   string   `json:"entrypoint"`

	// PidDomain scopes the process id, and its shape is platform specific:
	//
	//   Windows: "win32:<hostname in lower case>"
	//   Linux:   "linux:<machine-id>:<contents of /proc/self/ns/pid>"
	//
	// The Linux form encodes the process id namespace on purpose. A process id
	// only means something inside its namespace, so sessions in different
	// containers must not verify each other.
	PidDomain string `json:"pidDomain"`

	// MessagingSocketPath is the session inbox:
	//
	//   Windows: a named pipe.
	//   Linux:   $XDG_RUNTIME_DIR/cc-socks/<pid>.sock, with /tmp/cc-socks-<uid>
	//            as the documented fallback.
	MessagingSocketPath string `json:"messagingSocketPath"`

	Name            string `json:"name"`
	NameSource      string `json:"nameSource"`
	NameSince       int64  `json:"nameSince"`
	Status          string `json:"status"`
	UpdatedAt       int64  `json:"updatedAt"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
}

// Supported reports whether this build is willing to speak to the session that
// wrote the record. A false result is the signal to degrade rather than to guess.
func (r Record) Supported() bool {
	return r.PeerProtocol == PeerProtocol
}
