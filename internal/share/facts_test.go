package share

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alansav/filo/internal/tags"
)

// The gap the identity facts close: the desktop edits a tagged file, then moves it; the laptop's scan sees a missing
// file and a new one it cannot pair (other content, other path). The desktop's fact makes the laptop follow it, and
// the tag goes with it.
func TestPlaceFactFollowsAnEditedAndMovedFile(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, map[string]string{"bills/x.pdf": "old"})
	tg, _ := tags.Parse("sender:EDF")
	if _, err := b.s.TagFiles(ctx, []int64{b.file("bills/x.pdf")}, []tags.Tag{tg}, t0); err != nil {
		t.Fatal(err)
	}
	b.publish(t0)
	syncFilo(t, b, a)
	a.imp(t0)
	if got := a.tags("bills/x.pdf"); len(got) != 1 {
		t.Fatalf("a's tags before: %q", got)
	}
	// b edits it, scans twice (the edit is settled), moves it, scans.
	b.write("bills/x.pdf", "new")
	b.scan()
	b.scan()
	b.publish(t0.Add(time.Minute))
	if err := os.MkdirAll(filepath.Join(b.sync, "house"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(b.sync, "bills/x.pdf"), filepath.Join(b.sync, "house/gas.pdf")); err != nil {
		t.Fatal(err)
	}
	b.scan()
	b.publish(t0.Add(2 * time.Minute))
	// The sync brings both at once to a: the old file gone, the new one elsewhere.
	if err := os.Remove(filepath.Join(a.sync, "bills/x.pdf")); err != nil {
		t.Fatal(err)
	}
	a.write("house/gas.pdf", "new")
	a.scan()
	syncFilo(t, b, a)
	a.imp(t0.Add(3 * time.Minute)) // one import: the fact and the decisions it carries over, together
	if got := a.tags("house/gas.pdf"); len(got) != 1 || got[0] != "sender:EDF" {
		t.Fatalf("a's moved file's tags: %q", got)
	}
}

// An edit seen on one scan only is not a fact yet: it may be a file still being written.
func TestAnEditIsAFactOnlyOnceSettled(t *testing.T) {
	ctx := context.Background()
	_, b := pair(t, map[string]string{"x.pdf": "old"})
	tg, _ := tags.Parse("urgent")
	if _, err := b.s.TagFiles(ctx, []int64{b.file("x.pdf")}, []tags.Tag{tg}, t0); err != nil {
		t.Fatal(err)
	}
	b.publish(t0)
	before := b.lines(b.dev().ID)
	b.write("x.pdf", "half")
	b.scan()
	b.publish(t0.Add(time.Minute))
	if b.lines(b.dev().ID) != before {
		t.Fatal("an edit seen once was written as a fact")
	}
	b.scan()
	b.publish(t0.Add(2 * time.Minute))
	if b.lines(b.dev().ID) != before+1 {
		t.Fatalf("a settled edit was not written as a fact: %d lines, had %d", b.lines(b.dev().ID), before)
	}
}

// A content arriving by sync, which another device decided about, is not written back as an edit.
func TestArrivingContentIsNotAnEdit(t *testing.T) {
	a, b := pair(t, map[string]string{"x.pdf": "old"})
	a.write("x.pdf", "new")
	a.scan()
	a.set("x.pdf", "2026-01-01", "7y", t0) // a's decision names the new content
	syncFilo(t, a, b)
	b.imp(t0)
	before := b.lines(b.dev().ID)
	b.write("x.pdf", "new") // the sync brings it
	b.scan()
	b.imp(t0.Add(time.Minute))
	b.scan()
	b.publish(t0.Add(2 * time.Minute))
	if b.lines(b.dev().ID) != before {
		t.Fatalf("b wrote an edit fact for a content arriving by sync: %d lines, had %d", b.lines(b.dev().ID), before)
	}
}

// An edit seen once, then the file gone (deleted, moved out): the facts after it are not held up for ever.
func TestAnUnsettledEditDoesNotHoldUpLaterFacts(t *testing.T) {
	ctx := context.Background()
	_, b := pair(t, map[string]string{"x.pdf": "old", "y.pdf": "y"})
	tg, _ := tags.Parse("urgent")
	if _, err := b.s.TagFiles(ctx, []int64{b.file("x.pdf"), b.file("y.pdf")}, []tags.Tag{tg}, t0); err != nil {
		t.Fatal(err)
	}
	b.publish(t0)
	b.write("x.pdf", "half")
	b.scan() // x edited, seen once
	if err := os.Remove(filepath.Join(b.sync, "x.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(b.sync, "y.pdf"), filepath.Join(b.sync, "z.pdf")); err != nil {
		t.Fatal(err)
	}
	b.scan() // x gone, y moved
	before := b.lines(b.dev().ID)
	b.publish(t0.Add(time.Minute))
	if b.lines(b.dev().ID) != before+1 { // y's move, and nothing for x's edit
		t.Fatalf("facts after an unsettled edit: %d lines, had %d", b.lines(b.dev().ID), before)
	}
}
