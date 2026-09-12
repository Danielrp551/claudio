// Package identity holds the key material of a workspace member and seals
// messages so that only the intended recipient can read them.
//
// # The construction, and why it is boring on purpose
//
// Nothing here is invented. A member has two long term keys: an Ed25519 pair
// that signs, and an X25519 pair that agrees. To send a message, the sender
// generates a fresh X25519 key, agrees with the recipient's static key, derives
// a symmetric key with HKDF, encrypts with AES-256-GCM, and signs the result
// with its Ed25519 identity.
//
// Every primitive comes from the Go standard library, so this package has no
// dependencies at all. See ADR-0005.
//
// # What it protects and what it does not
//
// The relay routes envelopes and cannot read them. The signature means a
// recipient knows who wrote a message, and the derived key is bound to both
// parties so an envelope cannot be redirected to somebody else.
//
// There is no forward secrecy in version one. Compromising a member's long term
// agreement key would expose messages captured earlier. That is a deliberate
// limit, it is written in SECURITY.md rather than hidden, and closing it means a
// ratchet, which is its own decision record.
package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// hkdfInfo binds a derived key to this protocol and version, so a key derived
// here can never be mistaken for a key derived for something else.
const hkdfInfo = "claudio/v1 message key"

var (
	// ErrBadSignature means the envelope was not written by the sender it
	// claims, or it was altered on the way.
	ErrBadSignature = errors.New("identity: the envelope signature does not verify")

	// ErrCannotOpen means the envelope did not decrypt. It deliberately says
	// nothing about why.
	ErrCannotOpen = errors.New("identity: the envelope could not be opened")

	// ErrMalformed means the envelope is not shaped like one.
	ErrMalformed = errors.New("identity: malformed envelope")
)

// Public is the part of an identity that is published to a workspace.
type Public struct {
	// Signing is the Ed25519 public key, which is what a member is.
	Signing []byte `json:"signing"`
	// Agreement is the X25519 public key, which is what messages are sealed to.
	Agreement []byte `json:"agreement"`
}

// Fingerprint is a short, stable string a person can compare out of band. It is
// how two people confirm they added each other and not somebody else.
func (p Public) Fingerprint() string {
	sum := sha256.Sum256(append(append([]byte{}, p.Signing...), p.Agreement...))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

// Valid reports whether both keys are present and the right size.
func (p Public) Valid() bool {
	return len(p.Signing) == ed25519.PublicKeySize && len(p.Agreement) == 32
}

// Identity is a member's private key material.
type Identity struct {
	signing   ed25519.PrivateKey
	agreement *ecdh.PrivateKey
}

// Generate creates a fresh identity.
func Generate() (*Identity, error) {
	_, signing, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: generating a signing key: %w", err)
	}
	agreement, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: generating an agreement key: %w", err)
	}
	return &Identity{signing: signing, agreement: agreement}, nil
}

// Public returns what this identity publishes.
func (i *Identity) Public() Public {
	return Public{
		Signing:   i.signing.Public().(ed25519.PublicKey),
		Agreement: i.agreement.PublicKey().Bytes(),
	}
}

// Fingerprint is the short comparable form of this identity.
func (i *Identity) Fingerprint() string { return i.Public().Fingerprint() }

// Envelope is a sealed message. Only Ciphertext is secret, and the rest is what
// the recipient needs in order to open it.
type Envelope struct {
	// Sender is the Ed25519 public key of whoever sealed this.
	Sender []byte `json:"sender"`
	// Ephemeral is the one time X25519 public key for this envelope.
	Ephemeral  []byte `json:"ephemeral"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
	// Signature covers everything above plus the recipient, so an envelope
	// cannot be replayed towards a different member.
	Signature []byte `json:"signature"`
}

// Seal encrypts plaintext for one recipient and signs it.
func (i *Identity) Seal(to Public, plaintext []byte) (Envelope, error) {
	if !to.Valid() {
		return Envelope{}, fmt.Errorf("%w: the recipient key is not usable", ErrMalformed)
	}

	recipient, err := ecdh.X25519().NewPublicKey(to.Agreement)
	if err != nil {
		return Envelope{}, fmt.Errorf("identity: reading the recipient key: %w", err)
	}

	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Envelope{}, fmt.Errorf("identity: generating an ephemeral key: %w", err)
	}
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return Envelope{}, fmt.Errorf("identity: agreeing on a key: %w", err)
	}

	sender := i.Public()
	key, err := deriveKey(shared, sender.Signing, to.Agreement)
	if err != nil {
		return Envelope{}, err
	}

	aead, err := newAEAD(key)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, fmt.Errorf("identity: generating a nonce: %w", err)
	}

	env := Envelope{
		Sender:     sender.Signing,
		Ephemeral:  ephemeral.PublicKey().Bytes(),
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, nil),
	}
	env.Signature = ed25519.Sign(i.signing, signedBytes(env, to.Agreement))
	return env, nil
}

// Open verifies and decrypts an envelope sealed to this identity.
//
// It returns the Ed25519 public key of whoever sealed it, which is what a member
// is. The caller looks that key up among the members it knows and decides
// whether the sender is welcome. Open proves who wrote a message, it does not
// decide anything about trust.
//
// It deliberately does not return a Public. The envelope carries the sender's
// signing key but only an ephemeral agreement key, so a Public built from it
// would carry a value that changes with every message and would not match the
// member's published identity.
func (i *Identity) Open(env Envelope) (plaintext []byte, sender ed25519.PublicKey, err error) {
	if len(env.Sender) != ed25519.PublicKeySize ||
		len(env.Ephemeral) != 32 ||
		len(env.Signature) != ed25519.SignatureSize ||
		len(env.Nonce) == 0 ||
		len(env.Ciphertext) == 0 {
		return nil, nil, ErrMalformed
	}

	mine := i.Public()
	if !ed25519.Verify(env.Sender, signedBytes(env, mine.Agreement), env.Signature) {
		return nil, nil, ErrBadSignature
	}

	ephemeral, err := ecdh.X25519().NewPublicKey(env.Ephemeral)
	if err != nil {
		return nil, nil, ErrMalformed
	}
	shared, err := i.agreement.ECDH(ephemeral)
	if err != nil {
		return nil, nil, ErrCannotOpen
	}

	key, err := deriveKey(shared, env.Sender, mine.Agreement)
	if err != nil {
		return nil, nil, err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, nil, err
	}

	out, err := aead.Open(nil, env.Nonce, env.Ciphertext, nil)
	if err != nil {
		return nil, nil, ErrCannotOpen
	}
	return out, ed25519.PublicKey(env.Sender), nil
}

// signedBytes is what the signature covers.
//
// It includes the recipient's agreement key on purpose. Without it, an envelope
// could be handed to a different member, who would fail to decrypt but would
// still see a valid signature from the sender, which is a confusing and
// avoidable state.
func signedBytes(env Envelope, recipientAgreement []byte) []byte {
	h := sha256.New()
	for _, part := range [][]byte{
		[]byte(hkdfInfo), env.Sender, env.Ephemeral, env.Nonce, env.Ciphertext, recipientAgreement,
	} {
		// Length prefixing keeps concatenation unambiguous, so no combination of
		// parts can be rearranged into the same digest.
		if len(part) > math.MaxUint32 {
			// Unreachable with any envelope this protocol accepts, and a silent
			// truncation here would break the unambiguity the prefix exists for.
			panic("identity: a signed part is larger than the length prefix can carry")
		}
		var l [4]byte
		//nolint:gosec // the length is guarded against overflow immediately above
		binary.BigEndian.PutUint32(l[:], uint32(len(part)))
		h.Write(l[:])
		h.Write(part)
	}
	return h.Sum(nil)
}

func deriveKey(shared, senderSigning, recipientAgreement []byte) ([]byte, error) {
	salt := sha256.Sum256(append(append([]byte{}, senderSigning...), recipientAgreement...))
	key, err := hkdf.Key(sha256.New, shared, salt[:], hkdfInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("identity: deriving a message key: %w", err)
	}
	return key, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("identity: building a cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("identity: building an AEAD: %w", err)
	}
	return aead, nil
}

// ChallengeBytes is what a member signs to prove who it is to a relay.
//
// It is bound to the workspace the member asked to join, so a signature
// collected by one relay cannot be replayed at another. Both sides call this
// function rather than building the bytes themselves, because a binding the two
// ends disagree about is a binding that does nothing.
func ChallengeBytes(workspace string, nonce []byte) []byte {
	out := make([]byte, 0, len(workspace)+len(nonce)+32)
	out = append(out, []byte("claudio/v1 relay challenge ")...)
	out = append(out, []byte(workspace)...)
	out = append(out, ' ')
	return append(out, nonce...)
}

// SignChallenge answers a relay challenge.
func (i *Identity) SignChallenge(workspace string, nonce []byte) []byte {
	return ed25519.Sign(i.signing, ChallengeBytes(workspace, nonce))
}

// VerifyChallenge checks an answer to a relay challenge.
func VerifyChallenge(pub Public, workspace string, nonce, signature []byte) bool {
	if len(pub.Signing) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub.Signing, ChallengeBytes(workspace, nonce), signature)
}

// SameSigner reports whether a key identifies this member, in constant time.
func (p Public) SameSigner(key []byte) bool {
	return len(key) == ed25519.PublicKeySize &&
		subtle.ConstantTimeCompare(p.Signing, key) == 1
}

// Equal reports whether two public identities are the same, in constant time.
func Equal(a, b Public) bool {
	return subtle.ConstantTimeCompare(a.Signing, b.Signing) == 1 &&
		subtle.ConstantTimeCompare(a.Agreement, b.Agreement) == 1
}

// stored is the on disk shape of an identity.
type stored struct {
	Signing   []byte `json:"signing"`
	Agreement []byte `json:"agreement"`
}

// Save writes the identity to a file only this user can read.
func (i *Identity) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("identity: creating the key directory: %w", err)
	}
	blob, err := json.Marshal(stored{
		Signing:   i.signing,
		Agreement: i.agreement.Bytes(),
	})
	if err != nil {
		return fmt.Errorf("identity: encoding the identity: %w", err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return fmt.Errorf("identity: writing %s: %w", path, err)
	}
	return nil
}

// LoadOrCreate reads the identity at path, creating one if there is none.
func LoadOrCreate(path string) (*Identity, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		id, err := Generate()
		if err != nil {
			return nil, err
		}
		if err := id.Save(path); err != nil {
			return nil, err
		}
		return id, nil
	}
	if err != nil {
		return nil, fmt.Errorf("identity: reading %s: %w", path, err)
	}

	var s stored
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("identity: parsing %s: %w", path, err)
	}
	if len(s.Signing) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("identity: %s holds a signing key of the wrong size", path)
	}
	agreement, err := ecdh.X25519().NewPrivateKey(s.Agreement)
	if err != nil {
		return nil, fmt.Errorf("identity: %s holds an unusable agreement key: %w", path, err)
	}
	return &Identity{signing: ed25519.PrivateKey(s.Signing), agreement: agreement}, nil
}
