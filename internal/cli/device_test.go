package cli

import (
	"regexp"
	"strings"
	"testing"
)

// deviceLine returns the value after label in filo device's output ("    ID   <value>").
func deviceLine(t *testing.T, out, label string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^ +` + label + ` +(\S+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no %s line in:\n%s", label, out)
	}
	return m[1]
}

func TestDeviceShowsItsIdentity(t *testing.T) {
	ct := newCLITest(t)
	filo, contains := ct.filo, ct.contains
	out := filo(0, "", false, "device").stdout
	contains("device", out, "This device", "fingerprint", "what another device asks you to compare")
	id := deviceLine(t, out, "ID")
	if !regexp.MustCompile(`^[a-z2-7]{26}$`).MatchString(id) {
		t.Errorf("ID = %q, want 26 base32 letters", id)
	}
	fp := deviceLine(t, out, "fingerprint")
	if !regexp.MustCompile(`^[a-z2-7]{4}(-[a-z2-7]{4}){3}$`).MatchString(fp) {
		t.Errorf("fingerprint = %q, want four groups of four", fp)
	}
	again := filo(0, "", false, "device").stdout
	if again != out {
		t.Errorf("filo device changed between runs:\n%s\n%s", out, again)
	}
}

func TestDeviceName(t *testing.T) {
	ct := newCLITest(t)
	filo, contains := ct.filo, ct.contains
	id := deviceLine(t, filo(0, "", false, "device").stdout, "ID")
	contains("rename", filo(0, "", false, "device", "name", "desktop").stdout, "Device renamed", "→ desktop")
	out := filo(0, "", false, "device").stdout
	contains("after renaming", out, "This device   desktop")
	if deviceLine(t, out, "ID") != id {
		t.Error("renaming changed the ID")
	}
	contains("same name", filo(0, "", false, "device", "name", "desktop").stdout, "already named desktop")
	for _, bad := range [][]string{
		{"device", "name", "my desktop"},
		{"device", "name"},
		{"device", "name", "a", "b"},
		{"device", "rename", "x"},
		{"device", "name", "x", "-y"},
	} {
		filo(2, "", false, bad...)
	}
}

func TestDeviceNew(t *testing.T) {
	ct := newCLITest(t)
	filo, contains, lacks := ct.filo, ct.contains, ct.lacks
	filo(0, "", false, "device", "name", "copy")
	before := filo(0, "", false, "device").stdout
	id := deviceLine(t, before, "ID")

	// No terminal, no -y: nothing changes.
	out := filo(0, "", false, "device", "new").stdout
	contains("no terminal", out, "copied from another machine", "Nothing changed: there is no terminal")
	if deviceLine(t, filo(0, "", false, "device").stdout, "ID") != id {
		t.Fatal("device new without a terminal changed the ID")
	}
	// Declined at the terminal.
	out = filo(0, "n\n", true, "device", "new").stdout
	contains("declined", out, "Give this register a new device ID and key?", "Nothing changed.")
	if deviceLine(t, filo(0, "", false, "device").stdout, "ID") != id {
		t.Fatal("a declined device new changed the ID")
	}
	// Confirmed.
	out = filo(0, "y\n", true, "device", "new").stdout
	contains("confirmed", out, "New device identity   copy")
	after := filo(0, "", false, "device").stdout
	nid := deviceLine(t, after, "ID")
	if nid == id || deviceLine(t, after, "fingerprint") == deviceLine(t, before, "fingerprint") {
		t.Fatal("device new kept the ID or the key")
	}
	if !strings.Contains(out, nid) {
		t.Errorf("device new does not show the new ID %s:\n%s", nid, out)
	}
	// -y needs no terminal.
	out = filo(0, "", false, "device", "new", "-y").stdout
	lacks("-y", out, "Nothing changed")
	if deviceLine(t, filo(0, "", false, "device").stdout, "ID") == nid {
		t.Fatal("device new -y kept the ID")
	}
}
