package daemon

import (
	"path/filepath"
	"testing"

	"github.com/Danielrp551/claudio/internal/trust"
)

// TestHoldResolvesAsTheMostRestrictiveValue is the property that makes a gate a
// gate. If anything anywhere says hold, the message waits.
func TestHoldResolvesAsTheMostRestrictiveValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings TrustSettings
		proposed string
		want     string
	}{
		{
			name:     "a hold on one person holds",
			settings: TrustSettings{People: map[string]string{"luis": trust.Hold}},
			proposed: trust.Peer,
			want:     trust.Hold,
		},
		{
			name:     "a hold on the workspace holds everybody",
			settings: TrustSettings{Workspace: trust.Hold},
			proposed: trust.Peer,
			want:     trust.Hold,
		},
		{
			name:     "a hold on one session holds only there",
			settings: TrustSettings{Sessions: map[string]string{"prod": trust.Hold}},
			proposed: trust.Collaborator,
			want:     trust.Hold,
		},
		{
			name:     "no hold anywhere delivers as usual",
			settings: TrustSettings{Workspace: trust.Collaborator},
			proposed: trust.Peer,
			want:     trust.Collaborator,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := "prod"
			if tt.settings.Sessions == nil {
				session = "anything"
			}
			if got := tt.settings.For(tt.proposed, "luis", session); got != tt.want {
				t.Errorf("For = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestATypoDoesNotHoldEverything states the other half. Failing closed on trust
// means treating an unknown value as source, not as hold: a message nobody
// receives and nobody was asked about is indistinguishable from the tool being
// broken.
func TestATypoDoesNotHoldEverything(t *testing.T) {
	t.Parallel()

	settings := TrustSettings{People: map[string]string{"luis": "hodl"}}
	if got := settings.For(trust.Peer, "luis", ""); got != trust.Source {
		t.Errorf("a misspelled level resolved to %q, want %q", got, trust.Source)
	}
}

func TestHeldStoreSurvivesARestart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "held.json")

	first, err := newHeldStore(path)
	if err != nil {
		t.Fatalf("newHeldStore: %v", err)
	}
	id, err := first.Add(Delivery{
		From:      RemoteSession{Person: "luis", Session: "api"},
		ToSession: "herramientas",
		Text:      "revisa el schema",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// A message nobody has seen has to survive the connector restarting, or it
	// is withheld from the reader and gone before they were ever asked.
	second, err := newHeldStore(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	items, dropped := second.List()
	if len(items) != 1 {
		t.Fatalf("%d messages survived the restart, want 1", len(items))
	}
	if dropped != 0 {
		t.Errorf("dropped is %d, want 0", dropped)
	}
	if items[0].ID != id {
		t.Errorf("the identifier changed from %q to %q", id, items[0].ID)
	}
	if items[0].Delivery.Text != "revisa el schema" {
		t.Errorf("the text changed to %q", items[0].Delivery.Text)
	}

	took, err := second.Take(id)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if took.Delivery.From.Person != "luis" {
		t.Errorf("Take returned %q, want luis", took.Delivery.From.Person)
	}
	if _, err := second.Take(id); err == nil {
		t.Error("taking the same message twice succeeded")
	}

	third, err := newHeldStore(path)
	if err != nil {
		t.Fatalf("reopening again: %v", err)
	}
	if items, _ := third.List(); len(items) != 0 {
		t.Errorf("%d messages are still held after one was taken", len(items))
	}
}

// TestHeldStoreKeepsTheNewestAndSaysWhatItDropped covers the bound. A gate with
// no limit is somewhere a stranger can put unbounded data on your disk, and one
// that fills up silently is a gate that loses messages without saying so.
func TestHeldStoreKeepsTheNewestAndSaysWhatItDropped(t *testing.T) {
	t.Parallel()

	store, err := newHeldStore(filepath.Join(t.TempDir(), "held.json"))
	if err != nil {
		t.Fatalf("newHeldStore: %v", err)
	}

	const extra = 5
	for i := range heldLimit + extra {
		if _, err := store.Add(Delivery{Text: string(rune('a' + i%26))}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	items, dropped := store.List()
	if len(items) != heldLimit {
		t.Errorf("%d messages are held, want the limit of %d", len(items), heldLimit)
	}
	if dropped != extra {
		t.Errorf("dropped is %d, want %d", dropped, extra)
	}
}
