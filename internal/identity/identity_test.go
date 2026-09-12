package identity

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func TestSealAndOpen(t *testing.T) {
	t.Parallel()

	alice, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	bob, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	want := []byte("el schema cambio, tenant_id es la columna nueva")

	env, err := alice.Seal(bob.Public(), want)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	got, sender, err := bob.Open(env)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the plaintext came back as %q, want %q", got, want)
	}
	if !bytes.Equal(sender, alice.Public().Signing) {
		t.Error("Open reported the wrong sender")
	}
	// The returned key has to be the sender's stable identity, not something
	// that changes per message, because callers look members up by it.
	second, err := alice.Seal(bob.Public(), []byte("otro mensaje"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	_, senderAgain, err := bob.Open(second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(sender, senderAgain) {
		t.Error("Open reported a different sender for two messages from the same member")
	}
	if bytes.Contains(env.Ciphertext, want) {
		t.Error("the plaintext is visible in the ciphertext")
	}
}

// TestOpenRejectsTampering walks every field of an envelope and requires that
// changing any of them makes it fail to open. The point of an authenticated
// construction is that there is no field an attacker can move.
func TestOpenRejectsTampering(t *testing.T) {
	t.Parallel()

	alice, _ := Generate()
	bob, _ := Generate()

	tests := []struct {
		name   string
		damage func(*Envelope)
	}{
		{"the ciphertext", func(e *Envelope) { e.Ciphertext[0] ^= 0xff }},
		{"the nonce", func(e *Envelope) { e.Nonce[0] ^= 0xff }},
		{"the ephemeral key", func(e *Envelope) { e.Ephemeral[0] ^= 0xff }},
		{"the signature", func(e *Envelope) { e.Signature[0] ^= 0xff }},
		{"the claimed sender", func(e *Envelope) { e.Sender[0] ^= 0xff }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env, err := alice.Seal(bob.Public(), []byte("secreto"))
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			tt.damage(&env)

			if _, _, err := bob.Open(env); err == nil {
				t.Fatal("Open accepted a tampered envelope")
			}
		})
	}
}

// TestEnvelopeCannotBeRedirected is the reason the signature covers the
// recipient. Without that, a third member could be handed somebody else's
// envelope and would see a valid signature from a real sender, which is a
// confusing state that should simply not exist.
func TestEnvelopeCannotBeRedirected(t *testing.T) {
	t.Parallel()

	alice, _ := Generate()
	bob, _ := Generate()
	carol, _ := Generate()

	env, err := alice.Seal(bob.Public(), []byte("para bob"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, _, err := carol.Open(env); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Open by the wrong recipient returned %v, want ErrBadSignature", err)
	}
}

func TestOpenRejectsMalformedEnvelopes(t *testing.T) {
	t.Parallel()

	bob, _ := Generate()

	tests := []struct {
		name string
		env  Envelope
	}{
		{"empty", Envelope{}},
		{"no ciphertext", Envelope{
			Sender: make([]byte, 32), Ephemeral: make([]byte, 32),
			Nonce: make([]byte, 12), Signature: make([]byte, 64),
		}},
		{"a signing key of the wrong size", Envelope{
			Sender: make([]byte, 8), Ephemeral: make([]byte, 32),
			Nonce: make([]byte, 12), Ciphertext: []byte("x"), Signature: make([]byte, 64),
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, _, err := bob.Open(tt.env); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Open returned %v, want ErrMalformed", err)
			}
		})
	}
}

func TestFingerprintIsStableAndDistinct(t *testing.T) {
	t.Parallel()

	a, _ := Generate()
	b, _ := Generate()

	first, second := a.Fingerprint(), a.Fingerprint()
	if first != second {
		t.Errorf("a fingerprint must be stable, got %q then %q", first, second)
	}
	if a.Fingerprint() == b.Fingerprint() {
		t.Error("two identities must not share a fingerprint")
	}
	if a.Fingerprint() == "" {
		t.Error("a fingerprint must not be empty")
	}
}

func TestLoadOrCreate(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "keys", "identity.json")

	first, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	second, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}

	if first.Fingerprint() != second.Fingerprint() {
		t.Error("reloading an identity produced a different one")
	}

	// The reloaded identity has to be usable, not merely equal looking.
	other, _ := Generate()
	env, err := other.Seal(second.Public(), []byte("hola"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, _, err := second.Open(env); err != nil {
		t.Fatalf("the reloaded identity could not open an envelope: %v", err)
	}
}

func TestEqual(t *testing.T) {
	t.Parallel()

	a, _ := Generate()
	b, _ := Generate()

	if !Equal(a.Public(), a.Public()) {
		t.Error("an identity must equal itself")
	}
	if Equal(a.Public(), b.Public()) {
		t.Error("two identities must not be equal")
	}
}

func FuzzOpen(f *testing.F) {
	alice, _ := Generate()
	bob, _ := Generate()
	env, _ := alice.Seal(bob.Public(), []byte("hola"))

	f.Add(env.Sender, env.Ephemeral, env.Nonce, env.Ciphertext, env.Signature)

	f.Fuzz(func(_ *testing.T, sender, ephemeral, nonce, ciphertext, signature []byte) {
		// Open must return a value or an error for any input at all. A panic
		// here would be reachable by anybody who can reach the relay.
		_, _, _ = bob.Open(Envelope{
			Sender:     sender,
			Ephemeral:  ephemeral,
			Nonce:      nonce,
			Ciphertext: ciphertext,
			Signature:  signature,
		})
	})
}
