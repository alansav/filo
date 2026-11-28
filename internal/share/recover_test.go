package share

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

// reopen closes n's register and opens the database at db in its place.
func (n *node) reopen(db string) {
	n.t.Helper()
	n.s.Close()
	s, err := store.Open(context.Background(), store.Options{DBPath: db})
	if err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(func() { s.Close() })
	n.s = s
}

// A register restored from before some decisions finds its journal ahead; taking its own lines in brings the
// decisions back.
func TestRestoredRegisterTakesItsOwnLinesIn(t *testing.T) {
	ctx := context.Background()
	a, _ := pair(t, map[string]string{"x.pdf": "x"})
	db := a.s.DBPath()
	a.s.Close()
	saved := filepath.Join(t.TempDir(), "before.db")
	data, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(saved, data, 0o600); err != nil {
		t.Fatal(err)
	}
	a.reopen(db)
	tg, _ := tags.Parse("urgent")
	if _, err := a.s.TagFiles(ctx, []int64{a.file("x.pdf")}, []tags.Tag{tg}, t0); err != nil {
		t.Fatal(err)
	}
	a.publish(t0)
	// The register goes back to before the tag (restored from the copy); the journal keeps the line.
	a.s.Close()
	if err := os.WriteFile(db, data, 0o600); err != nil {
		t.Fatal(err)
	}
	os.Remove(db + "-wal")
	os.Remove(db + "-shm")
	a.reopen(db)
	if got := a.tags("x.pdf"); len(got) != 0 {
		t.Fatalf("the restored register has the tag already: %q", got)
	}
	dr := a.imp(t0.Add(time.Minute))
	if !dr.Ahead || dr.Writing {
		t.Fatalf("a restored register's journal is not found ahead: %+v", dr)
	}
	n, problems, err := TakeOwn(ctx, a.s, dr.Dir, t0.Add(time.Minute))
	if err != nil || n != 1 || len(problems) != 0 {
		t.Fatalf("TakeOwn = %d, %v", n, err)
	}
	if got := a.tags("x.pdf"); len(got) != 1 {
		t.Fatalf("the tag did not come back: %q", got)
	}
	if dr := a.imp(t0.Add(2 * time.Minute)); dr.Ahead || !dr.Writing || len(dr.Problems) != 0 {
		t.Fatalf("after taking its lines in: %+v", dr)
	}
}

// A copied register given a new ID starts a journal of its own in each synced directory; the old one stays, as
// another device's.
func TestNewIDStartsAJournal(t *testing.T) {
	ctx := context.Background()
	a, _ := pair(t, map[string]string{"x.pdf": "x"})
	old := a.dev().ID
	if _, err := a.s.NewDeviceIdentity(ctx); err != nil {
		t.Fatal(err)
	}
	dr := a.imp(t0)
	if len(dr.Notes) == 0 || !dr.Writing {
		t.Fatalf("after a new ID: %+v", dr)
	}
	for _, id := range []string{old, a.dev().ID} {
		if _, err := os.Stat(filepath.Join(a.sync, journal.FiloDir, journal.JournalName(id))); err != nil {
			t.Fatalf("journal %s: %v", id, err)
		}
	}
}

func TestRestoreWritesBackALostJournal(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	p := filepath.Join(b.sync, journal.FiloDir, journal.JournalName(a.dev().ID))
	want, _ := os.ReadFile(p)
	kd, _ := FindDevice(ctx, b.s, "laptop")
	in, _ := Where(ctx, b.s, kd.Dev)
	if done, err := Restore(ctx, b.s, kd, in); err != nil || len(done) != 0 {
		t.Fatalf("Restore of a journal that is fine: %v, %v", done, err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if need, _ := NeedsRestore(ctx, b.s, in[0], kd.Dev); !need {
		t.Fatal("a missing journal does not need restoring")
	}
	done, err := Restore(ctx, b.s, kd, in)
	if err != nil || len(done) != 1 {
		t.Fatalf("Restore = %v, %v", done, err)
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, want) {
		t.Fatal("the restored journal is not the one lost")
	}
	if _, err := Restore(ctx, b.s, store.KnownDevice{Dev: b.dev().ID}, in); err == nil {
		t.Fatal("restoring this device's own journal")
	}
}

// A decision made while this device's journal is ahead (blocked) is written once that is settled.
func TestDecisionMadeWhileBlockedIsWrittenLater(t *testing.T) {
	ctx := context.Background()
	a, _ := pair(t, map[string]string{"x.pdf": "x"})
	kept := mustLines(t, a, a.dev().ID)
	appendLine(t, a, kept, KindName, NameBody{Name: "laptop"}) // a line the register does not have: ahead
	if dr := a.imp(t0); !dr.Ahead {
		t.Fatal("not ahead")
	}
	if _, err := a.s.SetFiles(ctx, []int64{a.file("x.pdf")}, store.SetValues{Retention: "forever", SetRetention: true},
		t0, "2026-11-28", store.SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if problems, err := Publish(ctx, a.s, t0); err != nil || len(problems) == 0 {
		t.Fatalf("publishing while blocked: %v, %q", err, problems)
	}
	if _, _, err := TakeOwn(ctx, a.s, mustDir(t, a), t0); err != nil {
		t.Fatal(err)
	}
	before := a.lines(a.dev().ID)
	a.publish(t0.Add(time.Minute))
	if a.lines(a.dev().ID) == before {
		t.Fatal("the decision made while blocked was never written")
	}
}
