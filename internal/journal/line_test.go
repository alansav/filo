package journal

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/device"
)

var t0 = time.Date(2026, 11, 28, 21, 4, 11, 0, time.UTC)

// testDevice is a device ID and key for tests.
func testDevice(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	id, err := device.NewID()
	if err != nil {
		t.Fatal(err)
	}
	key, err := device.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return id, key
}

type testBody struct {
	Path string `json:"path"`
	N    int    `json:"n"`
}

func TestEncodeParseVerify(t *testing.T) {
	dev, key := testDevice(t)
	raw, err := Encode(key, Header{Dev: dev, Seq: 2, Prev: strings.Repeat("a", 64), Time: "2026-11-28T21:04:11Z",
		Kind: "test"}, testBody{Path: "bills/gas\tbill.pdf", N: 7})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte("\t")) != 1 || bytes.ContainsRune(raw, '\n') {
		t.Fatalf("a line must hold exactly one raw tab and no newline: %q", raw)
	}
	l, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if l.V != Version || l.Dev != dev || l.Seq != 2 || l.Kind != "test" {
		t.Fatalf("header = %+v", l.Header)
	}
	var b testBody
	if err := l.Body(&b); err != nil || b.Path != "bills/gas\tbill.pdf" || b.N != 7 {
		t.Fatalf("body = %+v, %v", b, err)
	}
	if !l.Verify(key.Public().(ed25519.PublicKey)) {
		t.Fatal("the line does not verify with its key")
	}
	_, other := testDevice(t)
	if l.Verify(other.Public().(ed25519.PublicKey)) {
		t.Fatal("the line verifies with another key")
	}
	// Any change to the bytes breaks the signature.
	bad := bytes.Replace(raw, []byte(`"n":7`), []byte(`"n":8`), 1)
	lb, err := Parse(bad)
	if err != nil {
		t.Fatal(err)
	}
	if lb.Verify(key.Public().(ed25519.PublicKey)) {
		t.Fatal("an edited line still verifies")
	}
}

func TestEncodeRefuses(t *testing.T) {
	dev, key := testDevice(t)
	h := Header{Dev: dev, Seq: 1, Kind: "test"}
	if _, err := Encode(key, h, map[string]string{"dev": "x"}); err == nil {
		t.Error("a body using a header key was encoded")
	}
	if _, err := Encode(key, h, []int{1}); err == nil {
		t.Error("a body that is not an object was encoded")
	}
	if _, err := Encode(key, h, testBody{Path: strings.Repeat("x", MaxLine)}); err == nil {
		t.Error("a line longer than MaxLine was encoded")
	}
}

func TestParseRefuses(t *testing.T) {
	dev, key := testDevice(t)
	sign := func(payload string) []byte {
		sig := ed25519.Sign(key, []byte(payload))
		return []byte(payload + "\t" + encodeSig(sig))
	}
	good := `{"v":1,"dev":"` + dev + `","seq":1,"prev":"","time":"x","kind":"device"}`
	if _, err := Parse(sign(good)); err != nil {
		t.Fatalf("a good line: %v", err)
	}
	tests := []struct {
		name string
		raw  []byte
	}{
		{"no tab", []byte(good)},
		{"short signature", []byte(good + "\tabc")},
		{"not JSON", sign(`not json`)},
		{"an array", sign(`[1,2]`)},
		{"a key twice", sign(`{"v":1,"dev":"` + dev + `","dev":"` + dev + `","seq":1,"prev":"","time":"x","kind":"k"}`)},
		{"two objects", sign(good + good)},
		{"bad UTF-8", sign(`{"v":1,"dev":"` + dev + "\xff" + `","seq":1,"prev":"","time":"x","kind":"k"}`)},
		{"no device", sign(`{"v":1,"seq":1,"prev":"","time":"x","kind":"k"}`)},
		{"bad device", sign(`{"v":1,"dev":"../x","seq":1,"prev":"","time":"x","kind":"k"}`)},
		{"seq 0", sign(`{"v":1,"dev":"` + dev + `","seq":0,"prev":"","time":"x","kind":"k"}`)},
		{"no kind", sign(`{"v":1,"dev":"` + dev + `","seq":1,"prev":"","time":"x"}`)},
		{"line 2 without prev", sign(`{"v":1,"dev":"` + dev + `","seq":2,"prev":"","time":"x","kind":"k"}`)},
		{"line 1 with prev", sign(`{"v":1,"dev":"` + dev + `","seq":1,"prev":"` + strings.Repeat("a", 64) + `","time":"x","kind":"k"}`)},
		{"prev not a hash", sign(`{"v":1,"dev":"` + dev + `","seq":2,"prev":"zz","time":"x","kind":"k"}`)},
		{"too long", sign(`{"v":1,"pad":"` + strings.Repeat("x", MaxLine) + `"}`)},
	}
	for _, tt := range tests {
		if _, err := Parse(tt.raw); err == nil {
			t.Errorf("%s: Parse succeeded", tt.name)
		}
	}
	_, err := Parse(sign(`{"v":2,"dev":"whatever","future":true}`))
	if !errors.Is(err, ErrNewer) {
		t.Errorf("a newer version: %v, want ErrNewer", err)
	}
}

func TestCheckPath(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{"bills/gas.pdf", true},
		{"a b/c\\d.pdf", true},
		{"€/x", true},
		{"", false},
		{"/etc/passwd", false},
		{"../x", false},
		{"a/../x", false},
		{"a/./x", false},
		{"a//x", false},
		{"a/", false},
		{"a\nb", false},
		{"a\x1b[31m", false},
		{"bad\xff", false},
		{strings.Repeat("a", MaxPath), true},
		{strings.Repeat("a", MaxPath+1), false},
	}
	for _, tt := range tests {
		if err := CheckPath(tt.path); (err == nil) != tt.ok {
			t.Errorf("CheckPath(%q) = %v, want ok %v", tt.path, err, tt.ok)
		}
	}
}

func encodeSig(sig []byte) string { return base64.StdEncoding.EncodeToString(sig) }
