package device

import (
	"crypto/ed25519"
	"errors"
	"regexp"
	"testing"
)

func TestNewIDIsRandomAndValid(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id, err := NewID()
		if err != nil {
			t.Fatal(err)
		}
		if !ValidID(id) {
			t.Fatalf("NewID() = %q, not a valid ID", id)
		}
		if seen[id] {
			t.Fatalf("NewID() gave %q twice", id)
		}
		seen[id] = true
	}
}

func TestValidID(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		id   string
		want bool
	}{
		{id, true},
		{"", false},
		{id[:25], false},
		{id + "a", false},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZ", false}, // upper case
		{"abcdefghijklmnopqrstuvwx01", false}, // 0 and 1 are not base32
		{"../../etc/passwd.xxxxxxxxx", false},
		{"aaaaaaaaaaaaaaaaaaaaaaaaab", false}, // the last character carries bits past the 128
	}
	for _, tt := range tests {
		if got := ValidID(tt.id); got != tt.want {
			t.Errorf("ValidID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

func TestKeyRoundTrip(t *testing.T) {
	priv, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeKey(EncodeKey(priv))
	if err != nil {
		t.Fatal(err)
	}
	if !back.Equal(priv) {
		t.Fatal("DecodeKey(EncodeKey(k)) is not k")
	}
	msg := []byte("a line")
	if !ed25519.Verify(back.Public().(ed25519.PublicKey), msg, ed25519.Sign(priv, msg)) {
		t.Fatal("a signature by the key does not verify with the decoded key")
	}
	pub, err := DecodePublic(EncodePublic(priv.Public().(ed25519.PublicKey)))
	if err != nil || !pub.Equal(priv.Public()) {
		t.Fatalf("DecodePublic(EncodePublic(p)) = %v, %v", pub, err)
	}
	for _, bad := range []string{"", "not base64!", "AAAA", EncodeKey(priv) + "AAAA"} {
		if _, err := DecodeKey(bad); err == nil {
			t.Errorf("DecodeKey(%q) succeeded", bad)
		}
		if _, err := DecodePublic(bad); err == nil {
			t.Errorf("DecodePublic(%q) succeeded", bad)
		}
	}
}

func TestFingerprint(t *testing.T) {
	a, _ := NewKey()
	b, _ := NewKey()
	fa := Fingerprint(a.Public().(ed25519.PublicKey))
	if !regexp.MustCompile(`^[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}$`).MatchString(fa) {
		t.Fatalf("Fingerprint = %q, not four groups of four", fa)
	}
	if fa != Fingerprint(a.Public().(ed25519.PublicKey)) {
		t.Fatal("Fingerprint is not stable")
	}
	if fa == Fingerprint(b.Public().(ed25519.PublicKey)) {
		t.Fatal("two keys have one fingerprint")
	}
}

func TestCheckName(t *testing.T) {
	long := ""
	for range MaxName {
		long += "a"
	}
	tests := []struct {
		name string
		ok   bool
	}{
		{"laptop", true},
		{"Desktop-2", true},
		{"old_desktop.home", true},
		{"9", true},
		{long, true},
		{long + "a", false},
		{"", false},
		{"-laptop", false},
		{".hidden", false},
		{"my laptop", false},
		{"laptop/2", false},
		{"lap\x1btop", false},
		{"läptop", false},
	}
	for _, tt := range tests {
		err := CheckName(tt.name)
		if (err == nil) != tt.ok {
			t.Errorf("CheckName(%q) = %v, want ok %v", tt.name, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrName) {
			t.Errorf("CheckName(%q) = %v, not ErrName", tt.name, err)
		}
	}
}

func TestDefaultName(t *testing.T) {
	tests := []struct {
		host string
		err  error
		want string
	}{
		{"laptop", nil, "laptop"},
		{"laptop.local", nil, "laptop"},
		{"", nil, "device"},
		{"weird host", nil, "device"},
		{"x", errors.New("no hostname"), "device"},
	}
	for _, tt := range tests {
		got := DefaultName(func() (string, error) { return tt.host, tt.err })
		if got != tt.want {
			t.Errorf("DefaultName(%q, %v) = %q, want %q", tt.host, tt.err, got, tt.want)
		}
	}
}
