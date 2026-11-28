package journal

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alansav/filo/internal/device"
)

func TestOpenAndMake(t *testing.T) {
	top := t.TempDir()
	if _, err := Open(top); !errors.Is(err, ErrNoFiloDir) {
		t.Fatalf("Open with no .filo: %v, want ErrNoFiloDir", err)
	}
	d, err := Make(top)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	fi, err := os.Stat(filepath.Join(top, FiloDir))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf(".filo after Make: %v, %v", fi, err)
	}
	d, err = Open(top)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Path() != filepath.Join(top, FiloDir) {
		t.Errorf("Path = %q", d.Path())
	}
}

func TestOpenRefusesALinkOrAFile(t *testing.T) {
	top := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(top, FiloDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(top); err == nil {
		t.Error("a .filo that is a symlink was opened")
	}
	if _, err := Make(top); err == nil {
		t.Error("Make followed a .filo that is a symlink")
	}
	top2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(top2, FiloDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(top2); err == nil {
		t.Error("a .filo that is a file was opened")
	}
}

func TestList(t *testing.T) {
	top := t.TempDir()
	d, err := Make(top)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	id, _ := device.NewID()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(top, FiloDir, name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{MarkName, JournalName(id), StatusName(id), "journal." + id + ".sync-conflict-1",
		"notes.txt", "journal.short", ".filo-abcdefghij.tmp"} {
		write(n)
	}
	if err := os.Mkdir(filepath.Join(top, FiloDir, StatusName("aaaaaaaaaaaaaaaaaaaaaaaaaa")), 0o700); err != nil {
		t.Fatal(err)
	}
	es, err := d.List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, e := range es {
		got[e.Name] = e
	}
	want := map[string]Entry{
		MarkName:                             {Name: MarkName, Kind: Mark},
		JournalName(id):                      {Name: JournalName(id), Kind: Journal, Dev: id},
		StatusName(id):                       {Name: StatusName(id), Kind: Status, Dev: id},
		"journal." + id + ".sync-conflict-1": {Name: "journal." + id + ".sync-conflict-1", Kind: Other, Dev: id, Note: "a copy of journal." + id},
		"notes.txt":                          {Name: "notes.txt", Kind: Other},
		"journal.short":                      {Name: "journal.short", Kind: Other},
		StatusName("aaaaaaaaaaaaaaaaaaaaaaaaaa"): {Name: StatusName("aaaaaaaaaaaaaaaaaaaaaaaaaa"), Kind: Other,
			Note: "not a regular file"},
	}
	if len(got) != len(want) {
		t.Errorf("List gave %d entries, want %d: %+v", len(got), len(want), es)
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("%s: %+v, want %+v", n, got[n], w)
		}
	}
}

func TestCreateAppendReadReplace(t *testing.T) {
	top := t.TempDir()
	d, err := Make(top)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	name := "journal.x"
	if err := d.Create(name, [][]byte{[]byte("one")}); err != nil {
		t.Fatal(err)
	}
	if err := d.Create(name, [][]byte{[]byte("again")}); err == nil {
		t.Fatal("Create replaced a file")
	}
	if err := d.Append(name, [][]byte{[]byte("two"), []byte("three")}); err != nil {
		t.Fatal(err)
	}
	b, err := d.Read(name, 0)
	if err != nil || string(b) != "one\ntwo\nthree\n" {
		t.Fatalf("Read = %q, %v", b, err)
	}
	if b, err := d.Read(name, 4); err != nil || string(b) != "two\nthree\n" {
		t.Fatalf("Read from 4 = %q, %v", b, err)
	}
	if b, err := d.Read(name, 100); err != nil || len(b) != 0 {
		t.Fatalf("Read past the end = %q, %v", b, err)
	}
	if err := d.Replace(name, [][]byte{[]byte("new")}); err != nil {
		t.Fatal(err)
	}
	if b, _ := d.Read(name, 0); string(b) != "new\n" {
		t.Fatalf("after Replace: %q", b)
	}
	fi, err := os.Stat(filepath.Join(top, FiloDir, name))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode after Replace: %v, %v", fi, err)
	}
	es, _ := d.List()
	if len(es) != 1 {
		t.Errorf("temporary files left after Replace: %+v", es)
	}
	if err := d.Append("missing", [][]byte{[]byte("x")}); err == nil {
		t.Error("Append made a file")
	}
}

func TestWritesRefuseLinks(t *testing.T) {
	top := t.TempDir()
	d, err := Make(top)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	victim := filepath.Join(top, "victim.txt") // a user's file inside the synced directory
	if err := os.WriteFile(victim, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../victim.txt", filepath.Join(top, FiloDir, "sym")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, filepath.Join(top, FiloDir, "hard")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sym", "hard"} {
		if err := d.Append(name, [][]byte{[]byte("x")}); err == nil {
			t.Errorf("Append to %s succeeded", name)
		}
		if err := d.Replace(name, [][]byte{[]byte("x")}); err == nil {
			t.Errorf("Replace of %s succeeded", name)
		}
		if _, err := d.Read(name, 0); err == nil {
			t.Errorf("Read of %s succeeded", name)
		}
	}
	if err := d.Create("sym", [][]byte{[]byte("x")}); err == nil {
		t.Error("Create over a symlink succeeded")
	}
	if b, _ := os.ReadFile(victim); string(b) != "mine\n" {
		t.Fatalf("the user's file changed: %q", b)
	}
}

func TestRecords(t *testing.T) {
	dev, key := testDevice(t)
	id, _ := device.NewID()
	raw, err := NewMark(key, dev, id, t0)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ReadRecord(append(raw, '\n'), KindMark)
	if err != nil {
		t.Fatal(err)
	}
	m, err := l.Mark()
	if err != nil || m.ID != id || l.Dev != dev || !l.Verify(key.Public().(ed25519.PublicKey)) {
		t.Fatalf("mark = %+v, %v", m, err)
	}
	if _, err := ReadRecord(append(raw, '\n'), KindStatus); err == nil {
		t.Error("a mark read as a status file")
	}
	if _, err := ReadRecord(raw, KindMark); err == nil {
		t.Error("a mark with no newline read")
	}
	if _, err := ReadRecord(append(append(raw, '\n'), append(raw, '\n')...), KindMark); err == nil {
		t.Error("a mark of two lines read")
	}
	st, err := NewStatus(key, dev, map[string]int64{dev: 4, "bad": 2}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sl, err := ReadRecord(append(st, '\n'), KindStatus)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := sl.Status()
	if err != nil || sb.Applied[dev] != 4 || len(sb.Applied) != 1 || sb.Seen != "2026-11-28T22:04:11Z" {
		t.Fatalf("status = %+v, %v", sb, err)
	}
}
