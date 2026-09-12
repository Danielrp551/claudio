package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

// newPolicyDaemon builds a daemon far enough to ask what it wants to run,
// without starting anything.
func newPolicyDaemon(t *testing.T, policy Policy, tr Transport) *Daemon {
	t.Helper()

	d, err := New(Options{
		SessionsDirs: []string{t.TempDir()},
		StatePath:    filepath.Join(t.TempDir(), "ghosts.json"),
		Executable:   "claudio",
		Workspace:    "acme",
		Policy:       policy,
		Transport:    tr,
		Logger:       quietLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// TestNothingIsPublishedBeforeTheRelayAcceptsUs is the regression test for a peer
// that promised what it could not deliver.
//
// A connector whose identity the relay refuses used to publish the workspace
// peer anyway. Every Claude Code session on that machine was then offered a
// workspace it had no access to, and anything sent to it was lost.
func TestNothingIsPublishedBeforeTheRelayAcceptsUs(t *testing.T) {
	t.Parallel()

	roster := []RemoteSession{{ID: "s-1", Person: "luis", Session: "api"}}

	refused := NewLoopback(roster, nil)
	refused.Unreachable = true
	d := newPolicyDaemon(t, Policy{Mode: ModeAuto}.SetCap(4), refused)

	if got := d.wanted(); len(got) != 0 {
		t.Errorf("a connector the relay never accepted wants to publish %v", got)
	}

	// The same connector, once it has been accepted, publishes as usual.
	accepted := newPolicyDaemon(t, Policy{Mode: ModeAuto}.SetCap(4), NewLoopback(roster, nil))
	if got := accepted.wanted(); len(got) == 0 {
		t.Error("a connector that is connected published nothing")
	}
}

// TestOffMeansNoProcesses is the regression test for a setting that did not do
// what its name says. The workspace peer used to be added before the mode was
// looked at, so somebody who asked for none still got one.
func TestOffMeansNoProcesses(t *testing.T) {
	t.Parallel()

	roster := []RemoteSession{{ID: "s-1", Person: "luis", Session: "api"}}
	d := newPolicyDaemon(t, Policy{Mode: ModeOff}, NewLoopback(roster, nil))

	if got := d.wanted(); len(got) != 0 {
		t.Errorf("off still wants to run %v", got)
	}
}

// TestACapOfZeroMeansZero covers the other half of the same promise. A cap of
// zero used to become the default of eight, because a configuration that never
// mentioned the cap looked identical to one that set it to zero.
func TestACapOfZeroMeansZero(t *testing.T) {
	t.Parallel()

	roster := []RemoteSession{
		{ID: "s-1", Person: "luis", Session: "api"},
		{ID: "s-2", Person: "ana", Session: "web"},
	}
	d := newPolicyDaemon(t, Policy{Mode: ModeAuto}.SetCap(0), NewLoopback(roster, nil))

	want := d.wanted()
	if len(want) != 1 {
		t.Fatalf("a cap of zero wants %d peers, want only the workspace one: %v", len(want), want)
	}
	if _, ok := want[WorkspacePeerName("acme")]; !ok {
		t.Errorf("the one peer left is not the workspace peer: %v", want)
	}

	// And a cap that was never set still means the default.
	unset := newPolicyDaemon(t, Policy{Mode: ModeAuto}, NewLoopback(roster, nil))
	if n := len(unset.wanted()); n != 3 {
		t.Errorf("an unset cap produced %d peers, want the two sessions plus the workspace", n)
	}
}

func TestCapResolvesTheThreeCases(t *testing.T) {
	t.Parallel()

	if got := (Policy{}).Cap(); got != DefaultMaxGhosts {
		t.Errorf("an unset cap is %d, want the default %d", got, DefaultMaxGhosts)
	}
	if got := (Policy{}).SetCap(0).Cap(); got != 0 {
		t.Errorf("a cap of zero is %d, want 0", got)
	}
	if got := (Policy{}).SetCap(-3).Cap(); got != 0 {
		t.Errorf("a negative cap is %d, want 0, because failing towards fewer processes is safe", got)
	}
}

// TestSettingsAreRereadWhileRunning is the regression test for trust changes that
// silently did nothing. Somebody who lowered the level for a person saw the old
// framing on the next message, with no explanation and no hint that a restart
// was required.
func TestSettingsAreRereadWhileRunning(t *testing.T) {
	t.Parallel()

	d := newPolicyDaemon(t, Policy{Mode: ModeAuto}.SetCap(4), NewLoopback(nil, nil))

	if got := d.opts.Trust.For("", "luis", ""); got != "collaborator" {
		t.Fatalf("the starting level is %q, want collaborator", got)
	}

	d.opts.Reload = func() (TrustSettings, Policy, error) {
		return TrustSettings{People: map[string]string{"luis": "source"}},
			Policy{Mode: ModeOff}, nil
	}
	d.reloadSettings()

	if got := d.opts.Trust.For("", "luis", ""); got != "source" {
		t.Errorf("after rereading, the level for luis is %q, want source", got)
	}
	if d.opts.Policy.Mode != ModeOff {
		t.Errorf("after rereading, the mode is %q, want off", d.opts.Policy.Mode)
	}
}

// TestAReplyReachesSomebodyWhoExposesNothing is the regression test for a
// conversation that could only go one way.
//
// The framing invites the reader to reply. A sender who exposes no session of
// their own is in nobody's roster, so the reply used to die in the connector
// with "could not work out who a message was for".
func TestAReplyReachesSomebodyWhoExposesNothing(t *testing.T) {
	t.Parallel()

	// An empty roster: nobody here exposes anything.
	d := newPolicyDaemon(t, Policy{Mode: ModeAuto}.SetCap(4), NewLoopback(nil, nil))

	if target, _ := d.resolveTarget(WorkspacePeerName("acme"), "@luis/api: hola"); target != "" {
		t.Fatalf("a stranger resolved to %q before they had written", target)
	}

	d.rememberSender(Delivery{
		From:    RemoteSession{Person: "luis", Session: "api"},
		ReplyTo: "member:abc123",
	})

	for _, mention := range []string{"@luis/api: hola", "@luis: hola", "@api: hola", "@luis-api: hola"} {
		target, rest := d.resolveTarget(WorkspacePeerName("acme"), mention)
		if target != "member:abc123" {
			t.Errorf("%q resolved to %q, want the address the message came from", mention, target)
		}
		if strings.TrimSpace(rest) != "hola" {
			t.Errorf("%q left the body as %q, want hola", mention, rest)
		}
	}
}
