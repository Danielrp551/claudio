package relay

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielrp551/claudio/internal/identity"
	"github.com/danielrp551/claudio/internal/workspace"
)

// JoinRequest is what a machine sends to redeem an invitation.
type JoinRequest struct {
	Workspace string          `json:"workspace"`
	Code      string          `json:"code"`
	Person    string          `json:"person"`
	Identity  identity.Public `json:"identity"`
}

// JoinResponse is what it gets back. It carries no secret, because the member's
// key never leaves the machine that generated it.
type JoinResponse struct {
	WorkspaceID string `json:"workspaceId"`
	Workspace   string `json:"workspace"`
	MemberID    string `json:"memberId"`
	Trust       string `json:"trust"`
	Fingerprint string `json:"fingerprint"`
}

// handleJoin redeems an invitation.
//
// It is the only endpoint a stranger can reach, so it says as little as
// possible. Every failure looks the same from outside, and nothing reveals
// whether a workspace exists, whether a code was ever real, or whether it was
// merely spent.
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}

	var req JoinRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "that request could not be read", http.StatusBadRequest)
		return
	}
	if !req.Identity.Valid() {
		http.Error(w, "that identity is not usable", http.StatusBadRequest)
		return
	}

	member, ws, err := s.store.Redeem(req.Code, req.Person, req.Identity)
	if err != nil {
		// One message for every reason, deliberately.
		s.log.Info("an invitation was not redeemed",
			"workspace", req.Workspace, "person", req.Person, "reason", err)
		http.Error(w, "that invitation cannot be redeemed", http.StatusForbidden)
		return
	}

	// The workspace the code belongs to is what counts, not the one the caller
	// named, so a code cannot be used to probe for other workspaces.
	if req.Workspace != "" && req.Workspace != ws.Slug {
		s.log.Warn("an invitation was redeemed against a different workspace than asked for",
			"asked", req.Workspace, "actual", ws.Slug)
	}

	s.log.Info("a member joined", "workspace", ws.Slug, "person", req.Person, "member", member.ID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(JoinResponse{
		WorkspaceID: ws.ID,
		Workspace:   ws.Slug,
		MemberID:    member.ID,
		Trust:       member.Trust,
		Fingerprint: member.Identity.Fingerprint(),
	})
}

// CreateWorkspace is the operator side of starting a workspace, used by the
// command that runs where the relay's store lives.
func CreateWorkspace(store *workspace.Store, slug, name, person string, who identity.Public) (*workspace.Workspace, *workspace.Member, error) {
	if !who.Valid() {
		return nil, nil, errors.New("relay: the owner identity is not usable")
	}
	return store.CreateWorkspace(slug, name, person, who)
}

// CreateInvitation is the operator side of inviting somebody.
func CreateInvitation(store *workspace.Store, slug string, ttl time.Duration, maxUses int) (string, error) {
	ws, err := store.WorkspaceBySlug(slug)
	if err != nil {
		return "", err
	}
	code, _, err := store.CreateInvitation(ws.ID, ws.OwnerID, ttl, maxUses)
	return code, err
}
