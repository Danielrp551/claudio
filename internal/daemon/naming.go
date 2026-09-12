package daemon

import (
	"regexp"
	"strings"
)

// Claude Code shows a session name in its agent list and lets a person mention
// it with an at sign. The typeahead requires double quotes around any name
// containing characters outside letters, digits, hyphens, and underscores, so a
// name like workspace:acme would have to be typed as @"workspace:acme".
//
// That is unacceptable for daily use, which is why peer names are generated from
// a template that only produces safe characters. See ADR-0003.
var unsafeForMention = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// PeerName builds the name a remote session appears under in the local agent
// list.
//
// The person is included, not only the session, because Claude Code renames
// duplicates with a variant suffix and a bare session name like api would
// collide the moment two people have one.
func PeerName(workspace string, s RemoteSession, includeWorkspace bool) string {
	parts := make([]string, 0, 3)
	if includeWorkspace && workspace != "" {
		parts = append(parts, workspace)
	}
	if s.Person != "" {
		parts = append(parts, s.Person)
	}
	if s.Session != "" {
		parts = append(parts, s.Session)
	}
	if len(parts) == 0 {
		parts = append(parts, s.ID)
	}
	return SafeName(strings.Join(parts, "-"))
}

// WorkspacePeerName is the name of the peer that stands for a whole workspace,
// which is how anything not promoted to its own peer stays reachable.
func WorkspacePeerName(workspace string) string {
	return SafeName("ws-" + workspace)
}

// SafeName reduces a string to what the mention typeahead accepts without
// quoting: letters, digits, hyphens, and underscores.
func SafeName(s string) string {
	out := unsafeForMention.ReplaceAllString(s, "-")
	out = strings.Trim(out, "-")
	if out == "" {
		return "peer"
	}
	// A name that is all separators after trimming would be useless, and a very
	// long one is unpleasant to type, so it is bounded.
	const maxLen = 48
	if len(out) > maxLen {
		out = strings.Trim(out[:maxLen], "-")
	}
	return out
}
