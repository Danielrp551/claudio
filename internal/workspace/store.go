package workspace

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Danielrp551/claudio/internal/identity"
	"github.com/Danielrp551/claudio/internal/safetext"
)

var (
	// ErrNotFound means nothing matched.
	ErrNotFound = errors.New("workspace: not found")
	// ErrExists means something with that name is already there.
	ErrExists = errors.New("workspace: already exists")
	// ErrInvitationUnusable means the code was real but is spent, expired, or
	// revoked. It is deliberately not distinguished from a wrong code in
	// anything a stranger can see.
	ErrInvitationUnusable = errors.New("workspace: that invitation cannot be redeemed")
	// ErrRevoked means the member exists but may no longer act.
	ErrRevoked = errors.New("workspace: that membership was revoked")
)

// Store holds a workspace and everything in it.
//
// Version one keeps the whole thing in memory and writes a JSON file after every
// change. A relay serves one small group of people, so the simplest durable
// thing that cannot lose data is the right amount of machinery. When that stops
// being true, this is the one type to replace.
type Store struct {
	path string

	// loadedAt and loadedSize describe the file as it was when this process last
	// read it, so a reader can tell in one stat whether somebody else has
	// written since.
	loadedAt   time.Time
	loadedSize int64

	mu          sync.RWMutex
	Workspaces  map[string]*Workspace  `json:"workspaces"`
	Members     map[string]*Member     `json:"members"`
	Invitations map[string]*Invitation `json:"invitations"`
	Machines    map[string]*Machine    `json:"machines"`
	Sessions    map[string]*Session    `json:"sessions"`
}

// OpenStore reads the store at path, creating an empty one if there is none.
func OpenStore(path string) (*Store, error) {
	s := &Store{
		path:        path,
		Workspaces:  map[string]*Workspace{},
		Members:     map[string]*Member{},
		Invitations: map[string]*Invitation{},
		Machines:    map[string]*Machine{},
		Sessions:    map[string]*Session{},
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace: reading %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, fmt.Errorf("workspace: parsing %s: %w", path, err)
	}
	s.stampLocked()
	return s, nil
}

// beginWrite opens a read, change and write cycle against the file.
//
// The mutex the caller already holds keeps the goroutines of this process out of
// each other's way. This is the other half: a lock every process shares, and a
// reread inside it. Both are needed. Without the lock, the relay and the command
// line interleave their writes and one of them disappears. Without the reread,
// the relay takes the lock and then writes a copy of the workspace it loaded at
// startup, which loses the same change with better timing.
//
// The returned function must be called when the cycle ends.
func (s *Store) beginWrite() (func(), error) {
	l, err := acquire(s.path)
	if err != nil {
		return nil, err
	}
	if err := s.reloadLocked(); err != nil {
		l.release()
		return nil, err
	}
	return l.release, nil
}

// refreshIfChangedLocked picks up what another process wrote, for a reader.
//
// No file lock here, and that is safe rather than sloppy: every write lands
// through a rename, so a reader sees the whole previous store or the whole new
// one and never a mixture. Checking the modification time and the size first
// means the common case, where nothing changed, costs one stat.
func (s *Store) refreshIfChangedLocked() {
	if s.path == "" {
		return
	}

	info, err := os.Stat(s.path)
	if err != nil {
		return
	}
	if info.ModTime().Equal(s.loadedAt) && info.Size() == s.loadedSize {
		return
	}
	if err := s.reloadLocked(); err != nil {
		// Answering from memory beats refusing to answer. The next write takes
		// the lock and reports the problem where it can be acted on.
		return
	}
}

// reloadLocked replaces what is in memory with what is on disk.
//
// A missing file is not an error: it is a store nobody has written yet, and the
// empty maps already in memory are the right answer.
func (s *Store) reloadLocked() error {
	if s.path == "" {
		return nil
	}

	s.stampLocked()

	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("workspace: rereading %s: %w", s.path, err)
	}

	// Decoded into a fresh value first, so a corrupt file cannot leave this
	// process holding half a store.
	fresh := &Store{
		Workspaces:  map[string]*Workspace{},
		Members:     map[string]*Member{},
		Invitations: map[string]*Invitation{},
		Machines:    map[string]*Machine{},
		Sessions:    map[string]*Session{},
	}
	if err := json.Unmarshal(raw, fresh); err != nil {
		return fmt.Errorf("workspace: parsing %s: %w", s.path, err)
	}

	s.Workspaces = fresh.Workspaces
	s.Members = fresh.Members
	s.Invitations = fresh.Invitations
	s.Machines = fresh.Machines
	s.Sessions = fresh.Sessions
	return nil
}

// persistLocked writes through a temporary file and renames, so a reader never
// sees half a store.
func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("workspace: creating the state directory: %w", err)
	}

	blob, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: encoding the store: %w", err)
	}
	// A fresh temporary name each time rather than a fixed one. The file lock
	// already keeps two writers apart, and this is what keeps the damage small
	// if it ever does not: with one shared name, two writers interleave inside
	// the same temporary file and the rename installs a store that is half of
	// each, which is a corrupt workspace rather than a lost change.
	f, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("workspace: creating a temporary store: %w", err)
	}
	tmp := f.Name()

	if _, err := f.Write(blob); err != nil {
		_ = f.Close()
		os.Remove(tmp)
		return fmt.Errorf("workspace: writing the store: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("workspace: closing the temporary store: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("workspace: tightening the temporary store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("workspace: installing the store: %w", err)
	}
	s.stampLocked()
	return nil
}

// stampLocked records what the file looks like now, so the next reader can tell
// whether anybody has written since without reading it again.
func (s *Store) stampLocked() {
	if s.path == "" {
		return
	}
	if info, err := os.Stat(s.path); err == nil {
		s.loadedAt = info.ModTime()
		s.loadedSize = info.Size()
	}
}

// CreateWorkspace makes a workspace and its owner in one step, because a
// workspace without an owner is not a state worth allowing.
func (s *Store) CreateWorkspace(slug, name, ownerPerson string, ownerIdentity identity.Public) (*Workspace, *Member, error) {
	if !ownerIdentity.Valid() {
		return nil, nil, errors.New("workspace: the owner identity is not usable")
	}
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return nil, nil, errors.New("workspace: a slug is required")
	}
	// The slug and both names end up inside sentences that other people read,
	// and the owner name ends up in the trust framing of every message this
	// member sends. They are refused here rather than cleaned, so what the store
	// holds is what the person who chose it believes it to be.
	if err := safetext.CheckField("workspace slug", slug); err != nil {
		return nil, nil, err
	}
	if err := safetext.CheckField("owner name", ownerPerson); err != nil {
		return nil, nil, err
	}
	if name != "" {
		if err := safetext.CheckField("workspace name", name); err != nil {
			return nil, nil, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	release, err := s.beginWrite()
	if err != nil {
		return nil, nil, err
	}
	defer release()

	for _, w := range s.Workspaces {
		if w.Slug == slug {
			return nil, nil, fmt.Errorf("%w: the workspace %q", ErrExists, slug)
		}
	}

	now := time.Now().UTC()
	w := &Workspace{
		ID:           newID(),
		Slug:         slug,
		Name:         cmpOr(name, slug),
		CreatedAt:    now,
		DefaultTrust: "collaborator",
	}
	owner := &Member{
		ID:          newID(),
		WorkspaceID: w.ID,
		Person:      ownerPerson,
		Identity:    ownerIdentity,
		Role:        RoleOwner,

		// The owner is proposed at the same level as everybody else. Owning a
		// workspace is about administering it, not about how much anybody's
		// Claude should trust the owner's messages. Giving the owner the highest
		// level here would grant it on every member's machine automatically,
		// which is exactly what ADR-0006 says must not happen.
		Trust: w.DefaultTrust,

		Status:   StatusActive,
		JoinedAt: now,
	}
	w.OwnerID = owner.ID

	s.Workspaces[w.ID] = w
	s.Members[owner.ID] = owner

	if err := s.persistLocked(); err != nil {
		return nil, nil, err
	}
	return w, owner, nil
}

// WorkspaceBySlug finds a workspace by the name people type.
func (s *Store) WorkspaceBySlug(slug string) (*Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshIfChangedLocked()

	slug = strings.ToLower(strings.TrimSpace(slug))
	for _, w := range s.Workspaces {
		if w.Slug == slug {
			return w, nil
		}
	}
	return nil, fmt.Errorf("%w: the workspace %q", ErrNotFound, slug)
}

// CreateInvitation returns the code to hand over and stores only its hash.
//
// A ttl of zero means the invitation does not expire. A negative ttl is an
// error rather than a synonym for zero, because silently turning "expired an
// hour ago" into "never expires" is exactly the kind of surprise that produces a
// workspace somebody can still join a year later.
func (s *Store) CreateInvitation(workspaceID, createdBy string, ttl time.Duration, maxUses int) (string, *Invitation, error) {
	if ttl < 0 {
		return "", nil, fmt.Errorf("workspace: an invitation cannot have a negative lifetime")
	}
	code, err := newInvitationCode()
	if err != nil {
		return "", nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	release, err := s.beginWrite()
	if err != nil {
		return "", nil, err
	}
	defer release()

	if _, ok := s.Workspaces[workspaceID]; !ok {
		return "", nil, fmt.Errorf("%w: workspace %s", ErrNotFound, workspaceID)
	}

	now := time.Now().UTC()
	inv := &Invitation{
		ID:          newID(),
		WorkspaceID: workspaceID,
		CodeHash:    hashCode(code),
		CreatedBy:   createdBy,
		CreatedAt:   now,
		MaxUses:     maxUses,
	}
	if ttl > 0 {
		inv.ExpiresAt = now.Add(ttl)
	}
	s.Invitations[inv.ID] = inv

	if err := s.persistLocked(); err != nil {
		return "", nil, err
	}
	return code, inv, nil
}

// Redeem exchanges a code for an individual membership.
//
// The same code redeemed twice produces two separate memberships with separate
// keys, each revocable on its own. That is the whole point: a code is a voucher,
// not a credential.
func (s *Store) Redeem(code, person string, who identity.Public) (*Member, *Workspace, error) {
	if !who.Valid() {
		return nil, nil, errors.New("workspace: the joining identity is not usable")
	}
	// The name a member chooses here is rendered inside the trust framing of
	// every message they ever send, on every machine that receives one. A
	// newline in it is enough to write a sentence of the framing yourself, so it
	// is refused at the door.
	if err := safetext.CheckField("person name", person); err != nil {
		return nil, nil, err
	}
	want := hashCode(code)

	s.mu.Lock()
	defer s.mu.Unlock()

	release, err := s.beginWrite()
	if err != nil {
		return nil, nil, err
	}
	defer release()

	now := time.Now().UTC()
	for _, inv := range s.Invitations {
		if subtle.ConstantTimeCompare([]byte(inv.CodeHash), []byte(want)) != 1 {
			continue
		}
		if why := inv.Unusable(now); why != "" {
			return nil, nil, fmt.Errorf("%w: %s", ErrInvitationUnusable, why)
		}

		// Somebody rejoining with the same identity gets their existing
		// membership back rather than a duplicate.
		for _, m := range s.Members {
			if m.WorkspaceID == inv.WorkspaceID && identity.Equal(m.Identity, who) {
				if !m.Active() {
					return nil, nil, ErrRevoked
				}
				return m, s.Workspaces[inv.WorkspaceID], nil
			}
		}

		w := s.Workspaces[inv.WorkspaceID]
		m := &Member{
			ID:          newID(),
			WorkspaceID: inv.WorkspaceID,
			Person:      person,
			Identity:    who,
			Role:        RoleMember,
			Trust:       w.DefaultTrust,
			Status:      StatusActive,
			InvitedBy:   inv.CreatedBy,
			JoinedAt:    now,
		}
		s.Members[m.ID] = m
		inv.Uses++

		if err := s.persistLocked(); err != nil {
			return nil, nil, err
		}
		return m, w, nil
	}
	return nil, nil, fmt.Errorf("%w: no invitation matches that code", ErrInvitationUnusable)
}

// MemberBySigning finds an active member by the key that identifies them.
func (s *Store) MemberBySigning(workspaceID string, signing []byte) (*Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshIfChangedLocked()

	for _, m := range s.Members {
		if m.WorkspaceID != workspaceID {
			continue
		}
		if subtle.ConstantTimeCompare(m.Identity.Signing, signing) == 1 {
			if !m.Active() {
				return nil, ErrRevoked
			}
			return m, nil
		}
	}
	return nil, ErrNotFound
}

// ListMembers lists everybody in a workspace, revoked included, because an audit
// that hides revoked members is not an audit.
func (s *Store) ListMembers(workspaceID string) []Member {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshIfChangedLocked()

	out := make([]Member, 0, len(s.Members))
	for _, m := range s.Members {
		if m.WorkspaceID == workspaceID {
			out = append(out, *m)
		}
	}
	return out
}

// Revoke ends a membership at once. Nothing belonging to anybody else changes.
func (s *Store) Revoke(memberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	release, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer release()

	m, ok := s.Members[memberID]
	if !ok {
		return fmt.Errorf("%w: member %s", ErrNotFound, memberID)
	}
	if m.Role == RoleOwner {
		return errors.New("workspace: the owner cannot be revoked")
	}

	now := time.Now().UTC()
	m.Status = StatusRevoked
	m.RevokedAt = &now

	// A revoked member's sessions stop being reachable immediately, rather than
	// lingering until something times out.
	for id, sess := range s.Sessions {
		if sess.MemberID == memberID {
			delete(s.Sessions, id)
		}
	}
	return s.persistLocked()
}

// SetTrust records what a workspace proposes for a member. The receiving machine
// still resolves the effective level by taking the most restrictive value.
func (s *Store) SetTrust(memberID, trust string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	release, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer release()

	m, ok := s.Members[memberID]
	if !ok {
		return fmt.Errorf("%w: member %s", ErrNotFound, memberID)
	}
	m.Trust = trust
	return s.persistLocked()
}

// ReplaceSessions sets the exposed sessions of one machine, which is how a
// connector reports what its user chose to share.
func (s *Store) ReplaceSessions(memberID string, machine Machine, sessions []Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	release, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer release()

	m, ok := s.Members[memberID]
	if !ok || !m.Active() {
		return fmt.Errorf("%w: member %s", ErrNotFound, memberID)
	}

	machine.MemberID = memberID
	machine.LastSeen = time.Now().UTC()
	if machine.ID == "" {
		machine.ID = newID()
	}
	s.Machines[machine.ID] = &machine

	for id, sess := range s.Sessions {
		if sess.MachineID == machine.ID {
			delete(s.Sessions, id)
		}
	}
	for i := range sessions {
		sess := sessions[i]
		sess.MachineID = machine.ID
		sess.MemberID = memberID
		sess.LastSeen = machine.LastSeen
		if sess.ID == "" {
			sess.ID = newID()
		}
		s.Sessions[sess.ID] = &sess
	}
	return s.persistLocked()
}

// DropMachine removes a machine and its sessions, which is what a connector
// disconnecting means for everybody else's roster.
func (s *Store) DropMachine(machineID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	release, err := s.beginWrite()
	if err != nil {
		return err
	}
	defer release()

	delete(s.Machines, machineID)
	for id, sess := range s.Sessions {
		if sess.MachineID == machineID {
			delete(s.Sessions, id)
		}
	}
	return s.persistLocked()
}

// Roster lists the exposed sessions of a workspace, excluding the ones belonging
// to the member asking, because nobody needs a native peer for their own session.
func (s *Store) Roster(workspaceID, excludeMemberID string) []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshIfChangedLocked()

	members := map[string]bool{}
	for _, m := range s.Members {
		if m.WorkspaceID == workspaceID && m.Active() {
			members[m.ID] = true
		}
	}

	out := make([]Session, 0, len(s.Sessions))
	for _, sess := range s.Sessions {
		if !members[sess.MemberID] || sess.MemberID == excludeMemberID {
			continue
		}
		out = append(out, *sess)
	}
	return out
}

// Member returns one member by identifier.
func (s *Store) Member(id string) (*Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshIfChangedLocked()

	m, ok := s.Members[id]
	if !ok {
		return nil, fmt.Errorf("%w: member %s", ErrNotFound, id)
	}
	return m, nil
}

// Session returns one exposed session by identifier.
func (s *Store) Session(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshIfChangedLocked()

	sess, ok := s.Sessions[id]
	if !ok {
		return nil, fmt.Errorf("%w: session %s", ErrNotFound, id)
	}
	return sess, nil
}

func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("id-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// invitationAlphabet leaves out the characters people confuse when reading a
// code aloud or copying it by hand.
var invitationAlphabet = base32.NewEncoding("ABCDEFGHJKLMNPQRSTUVWXYZ23456789").WithPadding(base32.NoPadding)

func newInvitationCode() (string, error) {
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("workspace: generating an invitation code: %w", err)
	}
	raw := invitationAlphabet.EncodeToString(b)

	// Grouping makes a code possible to read out over a call.
	var sb strings.Builder
	for i, r := range raw {
		if i > 0 && i%6 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteRune(r)
	}
	return sb.String(), nil
}

func hashCode(code string) string {
	normalised := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	sum := sha256.Sum256([]byte("claudio/v1 invitation " + normalised))
	return hex.EncodeToString(sum[:])
}

func cmpOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
