package share

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/device"
	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
)

// A retire line stops counting when its own device is cut before it: what it retired is not retired, whichever
// journal is read first.
func TestRetirementByACutDeviceDoesNotStand(t *testing.T) {
	bDev, bKey := testIdentity(t)
	lDev, lKey := testIdentity(t)
	line := func(key ed25519.PrivateKey, dev string, seq int64, kind string, body any) journal.Line {
		raw, err := journal.Encode(key, journal.Header{Dev: dev, Seq: seq, Prev: strings.Repeat("a", 64), Kind: kind},
			body)
		if err != nil {
			t.Fatal(err)
		}
		l, err := journal.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	// B retires L at L's line 3; L's line 10 retires B.
	bRead := func() *read {
		return &read{dev: bDev, lines: []journal.Line{line(bKey, bDev, 5, KindRetire, RetireBody{Device: lDev, Last: 3})}}
	}
	lRead := func() *read {
		var ls []journal.Line
		for seq := int64(2); seq <= 9; seq++ {
			ls = append(ls, line(lKey, lDev, seq, KindName, NameBody{Name: "laptop"}))
		}
		ls = append(ls, line(lKey, lDev, 10, KindRetire, RetireBody{Device: bDev, Last: 5}))
		return &read{dev: lDev, lines: ls}
	}
	for _, order := range [][]*read{{lRead(), bRead()}, {bRead(), lRead()}} {
		retired, _ := settle(order, map[string]store.JournalState{}, "me", map[string]store.KnownDevice{}, t0)
		if retired[bDev] != 0 || retired[lDev] != 3 {
			t.Fatalf("retired = %v: want L at 3 only", retired)
		}
	}
}

func testIdentity(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	dev, err := device.NewID()
	if err != nil {
		t.Fatal(err)
	}
	key, err := device.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return dev, key
}

// Rejoining a directory that holds this device's old journal: a new decision replaces its own old one, no conflict.
func TestRejoinReplacesOwnOldDecisions(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	b.set("x.pdf", "2026-01-01", "1y", t0)
	syncFilo(t, b, a)
	a.imp(t0)
	sd := mustDir(t, b)
	if err := b.s.RemoveSynced(ctx, sd.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.s.SetFiles(ctx, []int64{b.file("x.pdf")}, store.SetValues{Retention: "5y", SetRetention: true}, t0,
		"2026-11-28", store.SetOptions{Confirm: func(store.SetResult) bool { return true }}); err != nil {
		t.Fatal(err)
	}
	b.join()
	syncFilo(t, b, a)
	if dr := a.imp(t0.Add(time.Minute)); dr.Conflicts != 0 {
		t.Fatalf("b's new decision conflicts with its own old one on a: %d", dr.Conflicts)
	}
	if v := a.values("x.pdf"); v.Retention != "5y" {
		t.Fatalf("a's file: %+v", v)
	}
}

// Ending a set of records in a synced directory leaves alone a set with records outside it.
func TestRecordsRemovalStaysInside(t *testing.T) {
	ctx := context.Background()
	_, b := pair(t, map[string]string{"r.pdf": "rec"})
	out := filepath.Join(b.root, "r.pdf")
	if err := os.WriteFile(out, []byte("rec"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.scan()
	root, _ := b.s.RootAt(ctx, b.root)
	outside, _ := b.s.FileAt(ctx, root.ID, "r.pdf")
	if _, err := b.s.SetFiles(ctx, []int64{b.file("r.pdf"), outside.FileID}, store.SetValues{Duplicates: store.DupRecords},
		t0, "2026-11-28", store.SetOptions{}); err != nil {
		t.Fatal(err)
	}
	v := b.values("r.pdf")
	if _, _, err := applyRecords(ctx, b.s, mustDir(t, b), v.Hash, "", t0, "2026-11-28"); err != nil {
		t.Fatal(err)
	}
	if ms, _ := b.s.RecordMembers(ctx); len(ms) != 2 {
		t.Fatalf("a set with a record outside the synced directory was ended: %+v", ms)
	}
}

// A journal under this device's ID signed with another key is reported, and left as it is.
func TestOwnIDWithAnotherKeyIsNotOverwritten(t *testing.T) {
	a, _ := pair(t, map[string]string{"x.pdf": "x"})
	x := newNode(t, "intruder")
	_, first, err := journal.Start(x.dev().Key, a.dev().ID, "laptop", t0)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(a.sync, journal.FiloDir, journal.JournalName(a.dev().ID))
	if err := os.WriteFile(p, append(first, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	dr := a.imp(t0)
	if dr.Writing || len(dr.Problems) == 0 || !strings.Contains(dr.Problems[0], "another key") {
		t.Fatalf("a journal under this ID with another key: %+v", dr)
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, append(first, '\n')) {
		t.Fatal("it was overwritten")
	}
}

// A device retired in a directory that joins it again (its old journal lost) starts no journal there.
func TestRetiredDeviceStartsNoJournalOnJoining(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	kd, _ := FindDevice(ctx, a.s, "desktop")
	in, _ := Where(ctx, a.s, kd.Dev)
	if err := Retire(ctx, a.s, kd, in, t0); err != nil {
		t.Fatal(err)
	}
	if err := b.s.RemoveSynced(ctx, mustDir(t, b).ID); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(b.sync, journal.FiloDir, journal.JournalName(b.dev().ID))
	os.Remove(mine)
	os.Remove(filepath.Join(a.sync, journal.FiloDir, journal.JournalName(b.dev().ID)))
	syncFilo(t, a, b)
	os.Remove(mine)
	b.join()
	if _, err := os.Stat(mine); err == nil {
		t.Fatal("a device retired there started a journal on joining")
	}
}
