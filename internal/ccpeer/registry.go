package ccpeer

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// recordName is the shape Claude Code accepts for a session record. The number
// is a process id, and that is the identity: a record whose name does not match
// a live process of this user is ignored, and a process can therefore publish
// exactly one peer.
var recordName = regexp.MustCompile(`^\d+\.json$`)

// DefaultSessionsDir returns the session registry of the default Claude Code
// configuration directory.
func DefaultSessionsDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "sessions"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("ccpeer: locating the home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "sessions"), nil
}

// Registry reads and writes session records across one or more Claude Code
// configuration directories.
//
// Several profiles commonly point at the same directory through a symbolic
// link, which is how a user gets one set of peers across several accounts. The
// constructor resolves every directory and keeps each real one once, so that
// configuration does not turn into duplicated work and duplicated peers.
type Registry struct {
	dirs     []string
	platform LocalEndpoint
}

// NewRegistry resolves dirs, drops duplicates and unreadable entries, and
// returns a registry over what is left. It fails only when nothing is usable.
func NewRegistry(platform LocalEndpoint, dirs ...string) (*Registry, error) {
	if len(dirs) == 0 {
		d, err := DefaultSessionsDir()
		if err != nil {
			return nil, err
		}
		dirs = []string{d}
	}

	seen := make(map[string]bool, len(dirs))
	kept := make([]string, 0, len(dirs))
	var problems []error

	for _, d := range dirs {
		resolved, err := filepath.EvalSymlinks(d)
		if err != nil {
			// A directory that does not exist yet is not an error. Claude Code
			// creates it on its first run, and we may simply be early.
			if errors.Is(err, os.ErrNotExist) {
				resolved = filepath.Clean(d)
			} else {
				problems = append(problems, fmt.Errorf("%s: %w", d, err))
				continue
			}
		}
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		kept = append(kept, resolved)
	}

	if len(kept) == 0 {
		return nil, fmt.Errorf("ccpeer: no usable session directory: %w", errors.Join(problems...))
	}
	return &Registry{dirs: kept, platform: platform}, nil
}

// Dirs returns the resolved directories this registry reads, which is worth
// showing in diagnostics because symbolic links make it non obvious.
func (r *Registry) Dirs() []string {
	out := make([]string, len(r.dirs))
	copy(out, r.dirs)
	return out
}

// List returns every record that parses, is supported, and belongs to a live
// process.
//
// A record whose process is gone is skipped rather than reported. Claude Code
// leaves such records behind when a session dies abruptly, so they are normal
// rather than exceptional.
func (r *Registry) List() ([]Record, error) {
	var out []Record
	var problems []error

	for _, dir := range r.dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			problems = append(problems, fmt.Errorf("reading %s: %w", dir, err))
			continue
		}

		for _, e := range entries {
			if e.IsDir() || !recordName.MatchString(e.Name()) {
				continue
			}
			rec, err := readRecord(filepath.Join(dir, e.Name()))
			if err != nil {
				// A torn or corrupt record is expected while another process is
				// writing. Skipping it is correct, and the next poll picks it up.
				continue
			}
			if !rec.Supported() || !r.platform.ProcessAlive(rec.PID) {
				continue
			}
			out = append(out, rec)
		}
	}

	if len(out) == 0 && len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return out, nil
}

// Get returns the live record of one process id.
func (r *Registry) Get(pid int) (Record, error) {
	for _, dir := range r.dirs {
		rec, err := readRecord(filepath.Join(dir, strconv.Itoa(pid)+".json"))
		if err != nil {
			continue
		}
		if rec.PID != pid {
			continue
		}
		if !rec.Supported() {
			return Record{}, fmt.Errorf("%w: record of %d declares peerProtocol %d, this build speaks %d",
				ErrUnsupportedProtocol, pid, rec.PeerProtocol, PeerProtocol)
		}
		return rec, nil
	}
	return Record{}, fmt.Errorf("%w: pid %d", ErrNotFound, pid)
}

// Find returns the live record answering to a name.
//
// Names are not unique. When several live sessions share one, the caller has to
// disambiguate by process id, so this reports ErrAmbiguous rather than choosing.
func (r *Registry) Find(name string) (Record, error) {
	all, err := r.List()
	if err != nil {
		return Record{}, err
	}
	var matches []Record
	for _, rec := range all {
		if rec.Name == name {
			matches = append(matches, rec)
		}
	}
	switch len(matches) {
	case 0:
		return Record{}, fmt.Errorf("%w: %q", ErrNotFound, name)
	case 1:
		return matches[0], nil
	default:
		return Record{}, fmt.Errorf("%w: %q matches %d live sessions", ErrAmbiguous, name, len(matches))
	}
}

// Token returns the peer token of a session, which a platform that requires an
// authentication line needs in order to deliver.
//
// The key file name carries a hash whose derivation is not published, so the
// file is located by glob rather than by computing the name. That is enough for
// every real use, and it means a change in how Claude Code derives the hash does
// not break delivery.
func (r *Registry) Token(pid int) (string, error) {
	for _, dir := range r.dirs {
		matches, err := filepath.Glob(filepath.Join(dir, strconv.Itoa(pid)+".*.key"))
		if err != nil || len(matches) == 0 {
			continue
		}
		raw, err := os.ReadFile(matches[0])
		if err != nil {
			continue
		}
		var k struct {
			PeerToken string `json:"peerToken"`
		}
		if err := json.Unmarshal(raw, &k); err != nil || k.PeerToken == "" {
			continue
		}
		return k.PeerToken, nil
	}
	return "", fmt.Errorf("%w: pid %d", ErrNoKey, pid)
}

// Publication is the set of files this process wrote into a session directory,
// so a caller can remove exactly what it created and nothing else.
type Publication struct {
	RecordPath string
	KeyPath    string
	Record     Record

	// Token is the peer token published in the key file. The inbox of this
	// process checks an incoming authentication line against it.
	Token string
}

// Publish writes a session record for the current process.
//
// The caller supplies the name and the inbox path. Everything that has to be
// true about this process, meaning the process id, the start token and the
// process id domain, is read from the operating system here rather than passed
// in, because a caller that could supply those could supply wrong ones.
func (r *Registry) Publish(name, inboxPath string) (Publication, error) {
	if len(r.dirs) == 0 {
		return Publication{}, fmt.Errorf("ccpeer: no session directory to publish into")
	}
	dir := r.dirs[0]
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Publication{}, fmt.Errorf("ccpeer: creating %s: %w", dir, err)
	}

	pid := os.Getpid()

	start, err := r.platform.ProcStart(pid)
	if err != nil {
		return Publication{}, err
	}
	domain, err := r.platform.PidDomain()
	if err != nil {
		return Publication{}, err
	}
	cwd, _ := os.Getwd()
	now := time.Now().UnixMilli()

	rec := Record{
		PID:                 pid,
		SessionID:           newSessionID(),
		CWD:                 cwd,
		StartedAt:           now,
		ProcStart:           start,
		Version:             PublishedVersion,
		PeerProtocol:        PeerProtocol,
		PeerFeatures:        []string{},
		Kind:                "interactive",
		Entrypoint:          "cli",
		PidDomain:           domain,
		MessagingSocketPath: inboxPath,
		Name:                name,
		NameSource:          "derived",
		NameSince:           now,
		Status:              StatusIdle,
		UpdatedAt:           now,
		StatusUpdatedAt:     now,
	}

	recordPath := filepath.Join(dir, strconv.Itoa(pid)+".json")
	if err := writeRecord(recordPath, rec); err != nil {
		return Publication{}, err
	}

	keyPath, token, err := writeKey(dir, pid, start, domain)
	if err != nil {
		_ = os.Remove(recordPath)
		return Publication{}, err
	}

	return Publication{
		RecordPath: recordPath,
		KeyPath:    keyPath,
		Record:     rec,
		Token:      token,
	}, nil
}

// Update rewrites a published record, which is how status and liveness are
// refreshed. It returns the record it wrote.
func (r *Registry) Update(p Publication, status string) (Record, error) {
	rec := p.Record
	now := time.Now().UnixMilli()
	if status != "" && status != rec.Status {
		rec.Status = status
		rec.StatusUpdatedAt = now
	}
	rec.UpdatedAt = now
	if err := writeRecord(p.RecordPath, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Withdraw removes the files of a publication. It is safe to call more than
// once, which matters because it runs on several shutdown paths.
func (r *Registry) Withdraw(p Publication) error {
	var problems []error
	for _, f := range []string{p.RecordPath, p.KeyPath} {
		if f == "" {
			continue
		}
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			problems = append(problems, fmt.Errorf("removing %s: %w", f, err))
		}
	}
	return errors.Join(problems...)
}

func readRecord(path string) (Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, fmt.Errorf("ccpeer: parsing %s: %w", path, err)
	}
	if rec.PID == 0 || rec.MessagingSocketPath == "" {
		return Record{}, fmt.Errorf("ccpeer: %s is missing required fields", path)
	}
	return rec, nil
}

// writeRecord writes through a temporary file in the same directory and renames
// it, so a reader never observes half a record.
func writeRecord(path string, rec Record) error {
	blob, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("ccpeer: encoding a record: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".claudio-record-*")
	if err != nil {
		return fmt.Errorf("ccpeer: creating a temporary record: %w", err)
	}
	name := tmp.Name()

	if _, err := tmp.Write(blob); err != nil {
		_ = tmp.Close()
		os.Remove(name)
		return fmt.Errorf("ccpeer: writing a temporary record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("ccpeer: closing a temporary record: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return fmt.Errorf("ccpeer: tightening a temporary record: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("ccpeer: installing %s: %w", path, err)
	}
	return nil
}

// writeKey publishes a key file beside the record.
//
// The hash in the name is the one this project can derive, which is not
// necessarily the one Claude Code derives. That is fine: a sender that cannot
// find our key connects without an authentication line, and our inbox accepts
// that, which is the behaviour observed from a real session.
func writeKey(dir string, pid int, procStart, pidDomain string) (path, token string, err error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("ccpeer: generating a peer token: %w", err)
	}
	tok := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(tok))

	body, err := json.Marshal(map[string]string{
		"peerToken":   tok,
		"procStartFt": procStart,
		"pidDomain":   pidDomain,
	})
	if err != nil {
		return "", "", fmt.Errorf("ccpeer: encoding a key file: %w", err)
	}

	out := filepath.Join(dir, fmt.Sprintf("%d.%s.key", pid, hex.EncodeToString(sum[:])))
	if err := os.WriteFile(out, body, 0o600); err != nil {
		return "", "", fmt.Errorf("ccpeer: writing %s: %w", out, err)
	}
	return out, tok, nil
}

func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
