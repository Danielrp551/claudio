package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Danielrp551/claudio/internal/trust"
	"github.com/Danielrp551/claudio/internal/workspace"
)

// runWorkspaceRevoke ends a membership.
//
// It exists because the tool promised it and could not do it. The invite command
// tells the person creating a code that the membership it becomes can be revoked
// on its own, the architecture document describes revocable memberships, and
// Store.Revoke was written and tested. Nothing called it, so the only way to
// remove somebody was to stop the relay and edit the JSON by hand.
func runWorkspaceRevoke(_ context.Context, env Env, args []string) error {
	fs := flagSet("workspace revoke", env.Stderr)
	slug := fs.String("workspace", "", "the workspace to revoke from")
	storePath := fs.String("store", "", "where the workspace is kept")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: claudio workspace revoke <person> --workspace <slug>")
	}

	store, ws, err := openWorkspace(*storePath, *slug)
	if err != nil {
		return err
	}
	member, err := memberByPerson(store, ws.ID, fs.Arg(0))
	if err != nil {
		return err
	}

	if err := store.Revoke(member.ID); err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "revoked %s in %s\n", member.Person, ws.Slug)
	fmt.Fprintf(env.Stdout, "  their fingerprint was %s\n", member.Identity.Fingerprint())
	fmt.Fprintln(env.Stdout,
		"\ntheir sessions stopped being reachable at once, and their key is refused\n"+
			"from now on. Nothing they were already sent is recalled.")
	return nil
}

// runWorkspaceTrust changes the level the workspace proposes for a member.
//
// A proposal is all it is. Every machine resolves the level it actually uses by
// taking the most restrictive of every opinion, including its own, so this can
// lower somebody everywhere and can never raise them past what their own
// recipients allow. See ADR-0006.
func runWorkspaceTrust(_ context.Context, env Env, args []string) error {
	fs := flagSet("workspace trust", env.Stderr)
	slug := fs.String("workspace", "", "the workspace to change")
	storePath := fs.String("store", "", "where the workspace is kept")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: claudio workspace trust <person> <level> --workspace <slug>")
	}

	level, err := trust.Parse(fs.Arg(1))
	if err != nil {
		return err
	}

	store, ws, err := openWorkspace(*storePath, *slug)
	if err != nil {
		return err
	}
	member, err := memberByPerson(store, ws.ID, fs.Arg(0))
	if err != nil {
		return err
	}

	if err := store.SetTrust(member.ID, level); err != nil {
		return err
	}

	fmt.Fprintf(env.Stdout, "%s now proposes %q for %s\n", ws.Slug, level, member.Person)
	fmt.Fprintln(env.Stdout,
		"\nthat is a proposal. Every machine takes the most restrictive of it and its\n"+
			"own settings, so this can lower somebody everywhere and never raise them\n"+
			"above what a recipient allows.")
	return nil
}

// openWorkspace resolves the store and one workspace inside it.
func openWorkspace(storePath, slug string) (*workspace.Store, *workspace.Workspace, error) {
	if slug == "" {
		return nil, nil, errors.New("this needs --workspace")
	}
	path, err := storeOrDefault(storePath)
	if err != nil {
		return nil, nil, err
	}
	store, err := workspace.OpenStore(path)
	if err != nil {
		return nil, nil, err
	}
	ws, err := store.WorkspaceBySlug(slug)
	if err != nil {
		return nil, nil, err
	}
	return store, ws, nil
}

// memberByPerson finds one member by the name other people see.
//
// Names are what a person has in front of them, and identifiers are not. An
// ambiguous name is an error rather than a guess, because picking one of two
// people to revoke is not a mistake worth making on somebody's behalf.
func memberByPerson(store *workspace.Store, workspaceID, person string) (*workspace.Member, error) {
	var found []workspace.Member
	for _, m := range store.ListMembers(workspaceID) {
		if strings.EqualFold(m.Person, person) {
			found = append(found, m)
		}
	}

	switch len(found) {
	case 0:
		return nil, fmt.Errorf("nobody in this workspace is called %q", person)
	case 1:
		m := found[0]
		return &m, nil
	default:
		fingerprints := make([]string, 0, len(found))
		for _, m := range found {
			fingerprints = append(fingerprints, m.Identity.Fingerprint())
		}
		return nil, fmt.Errorf("%d members are called %q, with fingerprints %s",
			len(found), person, strings.Join(fingerprints, ", "))
	}
}
