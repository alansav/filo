// Package device is a device's identity for sharing decisions between devices (docs/design.md, Sharing decisions
// between devices): a random device ID, an ed25519 signing key and a name. A register has one of each, kept in its
// meta table; this package makes and checks them and knows nothing of the register.
package device

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// IDLen is the length of a device ID: 128 random bits in lower-case base32, without padding.
const IDLen = 26

// MaxName is the longest device name, in bytes.
const MaxName = 64

// encoding is base32 in lower case, without padding: device IDs and fingerprints use letters and the digits 2 to 7
// only, so they are safe in file names and easy to read aloud.
var encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// ErrName says what a device name may hold.
var ErrName = errors.New("a device name is 1 to 64 letters, digits, '.', '-' and '_', starting with a letter or digit")

// NewID returns a new random device ID.
func NewID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("make a device ID: %w", err)
	}
	return encoding.EncodeToString(b), nil
}

// ValidID reports whether s has the form of a device ID.
func ValidID(s string) bool {
	if len(s) != IDLen {
		return false
	}
	b, err := encoding.DecodeString(s)
	return err == nil && len(b) == 16 && encoding.EncodeToString(b) == s
}

// NewKey returns a new ed25519 private key.
func NewKey() (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("make a device key: %w", err)
	}
	return priv, nil
}

// EncodeKey writes a private key as it is stored: its 32-byte seed in standard base64.
func EncodeKey(priv ed25519.PrivateKey) string { return base64.StdEncoding.EncodeToString(priv.Seed()) }

// DecodeKey reads a private key written by EncodeKey.
func DecodeKey(s string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("the device key is not a valid ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// EncodePublic writes a public key as journals carry it: standard base64.
func EncodePublic(pub ed25519.PublicKey) string { return base64.StdEncoding.EncodeToString(pub) }

// DecodePublic reads a public key written by EncodePublic.
func DecodePublic(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("not a valid ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}

// Fingerprint is what a person compares to check a device's key: the first 80 bits of the SHA-256 of the public
// key, in lower-case base32, in four groups of four ("k3j7-2mqd-6x4p-wv5a").
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	s := encoding.EncodeToString(sum[:10])
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
}

// CheckName reports whether name can be a device name (ErrName if not).
func CheckName(name string) error {
	if name == "" || len(name) > MaxName {
		return ErrName
	}
	for i, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case (c == '.' || c == '-' || c == '_') && i > 0:
		default:
			return ErrName
		}
	}
	return nil
}

// DefaultName is the name a new device takes: its host name up to the first dot ("laptop" for laptop.local), or
// "device" when there is none or it cannot be a device name.
func DefaultName(hostname func() (string, error)) string {
	h, err := hostname()
	if err != nil {
		return "device"
	}
	h, _, _ = strings.Cut(h, ".")
	if CheckName(h) != nil {
		return "device"
	}
	return h
}
