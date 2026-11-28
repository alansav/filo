// Package journal is the files of a synced directory's .filo/ (docs/design.md, Sharing decisions between devices):
// each device's signed, append-only journal of decisions, its status file, and the synced directory's mark. It
// writes and reads lines, signs and checks them, and keeps them inside the synced directory; what a line decides is
// for its callers. It is the only code that writes .filo/ (TestOnlyJournalWritesSyncedFiles).
package journal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alansav/filo/internal/device"
)

// Version is the journal format this build writes and reads. A line of a newer version stops its journal there:
// what follows is not understood, so nothing of it is applied.
const Version = 1

// MaxLine is the longest line, in bytes, its newline not counted.
const MaxLine = 64 << 10

// MaxPath is the longest path a line may name, in bytes.
const MaxPath = 4096

// sigLen is the length of a signature in base64: 64 bytes.
var sigLen = base64.StdEncoding.EncodedLen(ed25519.SignatureSize)

// KindDevice is line 1 of every journal: the device introducing itself.
const KindDevice = "device"

// Header is what every line has, before what it is about.
type Header struct {
	V    int    `json:"v"`
	Dev  string `json:"dev"`
	Seq  int64  `json:"seq"`
	Prev string `json:"prev"` // the SHA-256 of the previous whole line, hex; "" on line 1
	Time string `json:"time"` // RFC 3339, UTC; shown only, never used to decide
	Kind string `json:"kind"`
}

// reserved are the header's keys, which a line's body may not use.
var reserved = map[string]bool{"v": true, "dev": true, "seq": true, "prev": true, "time": true, "kind": true}

// DeviceBody is the body of line 1: the device's ID is the header's dev.
type DeviceBody struct {
	Key     string `json:"key"` // the public key, base64
	Name    string `json:"name"`
	Created string `json:"created"`
}

// Line is one line as read and checked: its header, its whole bytes (without the newline, as the register keeps
// them), and its JSON, for the caller to read the body from.
type Line struct {
	Header
	Raw     []byte
	Payload []byte
}

// Hash is the line's SHA-256, hex: the next line's prev.
func (l Line) Hash() string { return HashOf(l.Raw) }

// Body decodes the line's body into v (unknown keys ignored).
func (l Line) Body(v any) error { return json.Unmarshal(l.Payload, v) }

// HashOf is a whole line's SHA-256, hex.
func HashOf(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Encode makes one signed line, without its newline: the header and body as one JSON object, a tab, and the
// base64 signature of exactly those bytes. body must encode as a JSON object whose keys are not the header's.
func Encode(key ed25519.PrivateKey, h Header, body any) ([]byte, error) {
	if h.V == 0 {
		h.V = Version
	}
	head, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	payload := head
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		keys, err := objectKeys(b)
		if err != nil {
			return nil, fmt.Errorf("encode a %s line: %w", h.Kind, err)
		}
		for _, k := range keys {
			if reserved[k] {
				return nil, fmt.Errorf("encode a %s line: the body uses the header's key %q", h.Kind, k)
			}
		}
		if len(keys) > 0 {
			payload = append(append(head[:len(head)-1:len(head)-1], ','), b[1:]...)
		}
	}
	raw := append(append(payload, '\t'), base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))...)
	if len(raw) > MaxLine {
		return nil, fmt.Errorf("encode a %s line: %d bytes, more than a line may hold", h.Kind, len(raw))
	}
	return raw, nil
}

// objectKeys returns the keys of the JSON object b, refusing anything else, a key that is not plain (lower-case
// letters, digits and '_': encoding/json matches keys ignoring case, so "Kind" or "KIND" would be read as "kind"),
// and a key given twice (a reader that kept the first and one that kept the last would read two different lines).
func objectKeys(b []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var keys []string
	seen := map[string]bool{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k := t.(string)
		if !plainKey(k) {
			return nil, fmt.Errorf("the key %q is not lower-case letters, digits and '_'", k)
		}
		if seen[k] {
			return nil, fmt.Errorf("the key %q is given twice", k)
		}
		seen[k] = true
		keys = append(keys, k)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more after the object")
	}
	if _, err := dec.Token(); err == nil {
		return nil, errors.New("more after the object")
	}
	return keys, nil
}

// plainKey reports whether k is a key a line may have: lower-case ASCII letters, digits and '_', none other, so no
// two keys are the same key to encoding/json.
func plainKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// ErrNewer is a line of a newer format than this build reads.
var ErrNewer = errors.New("made by a newer Filo")

// Parse splits a line (without its newline) into its header and JSON, checking its form but not its signature:
// at most MaxLine bytes, one JSON object of valid UTF-8 with no key twice, a tab, a signature of the right length.
// A line of a newer version is ErrNewer, after only what every version shares (the v key) is read.
func Parse(raw []byte) (Line, error) {
	if len(raw) > MaxLine {
		return Line{}, fmt.Errorf("a line of %d bytes, more than %d", len(raw), MaxLine)
	}
	i := bytes.LastIndexByte(raw, '\t')
	if i < 0 {
		return Line{}, errors.New("a line with no signature")
	}
	payload, sig := raw[:i], raw[i+1:]
	if len(sig) != sigLen {
		return Line{}, errors.New("a line whose signature is not one")
	}
	if !utf8.Valid(payload) {
		return Line{}, errors.New("a line that is not valid UTF-8")
	}
	if _, err := objectKeys(payload); err != nil {
		return Line{}, fmt.Errorf("a line that is not one JSON object: %w", err)
	}
	var v struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(payload, &v); err != nil {
		return Line{}, fmt.Errorf("a line with no format version: %w", err)
	}
	if v.V > Version {
		return Line{}, fmt.Errorf("format version %d: %w", v.V, ErrNewer)
	}
	var h Header
	if err := json.Unmarshal(payload, &h); err != nil {
		return Line{}, fmt.Errorf("a line whose header cannot be read: %w", err)
	}
	if h.V < 1 || !device.ValidID(h.Dev) || h.Seq < 1 || h.Kind == "" {
		return Line{}, errors.New("a line whose header is not complete")
	}
	if (h.Seq == 1) != (h.Prev == "") || (h.Prev != "" && !validHash(h.Prev)) {
		return Line{}, errors.New("a line whose prev is not a line's hash")
	}
	return Line{Header: h, Raw: raw, Payload: payload}, nil
}

// Verify checks the line's signature with pub.
func (l Line) Verify(pub ed25519.PublicKey) bool {
	if len(l.Raw) <= len(l.Payload) || len(pub) != ed25519.PublicKeySize {
		return false // not a line from Parse
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(string(l.Raw[len(l.Payload)+1:]))
	return err == nil && ed25519.Verify(pub, l.Payload, sig)
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// CheckPath reports whether p can be a path in a line: relative to the synced directory's top, '/'-separated,
// with no empty, "." or ".." part, valid UTF-8 with no control character, and at most MaxPath bytes. Paths are only
// ever matched against the register, never joined onto the disk; this keeps a hostile line from naming anything
// outside the synced directory all the same.
func CheckPath(p string) error {
	switch {
	case p == "":
		return errors.New("an empty path")
	case len(p) > MaxPath:
		return fmt.Errorf("a path of %d bytes, more than %d", len(p), MaxPath)
	case !utf8.ValidString(p):
		return errors.New("a path that is not valid UTF-8")
	case strings.HasPrefix(p, "/"):
		return errors.New("an absolute path")
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return errors.New("a path with a control character")
		}
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("a path with a %q part", part)
		}
	}
	return nil
}

// joinLines is lines, each followed by its newline: a journal's bytes.
func joinLines(lines [][]byte) []byte {
	n := 0
	for _, l := range lines {
		n += len(l) + 1
	}
	b := make([]byte, 0, n)
	for _, l := range lines {
		b = append(append(b, l...), '\n')
	}
	return b
}
