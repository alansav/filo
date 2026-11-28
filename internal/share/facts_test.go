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
	os.MkdirAll(filepath.Join(b.sync, "house"), 0o755)
	os.Rename(filepath.Join(b.sync, "bills/x.pdf"), filepath.Join(b.sync, "house/gas.pdf"))
	b.scan()
	b.publish(t0.Add(2 * time.Minute))
	// The sync brings both at once to a: the old file gone, the new one elsewhere.
	os.Remove(filepath.Join(a.sync, "bills/x.pdf"))
	a.write("house/gas.pdf", "new")
	a.scan()
	syncFilo(t, b, a)
	a.imp(t0.Add(3 * time.Minute))
	a.imp(t0.Add(4 * time.Minute))
	if got := a.tags("house/gas.pdf"); len(got) != 1 || got[0] != "sender:EDF" {
		t.Fatalf("a's moved file's tags: %q", got)
	}
}

// An edit seen on one scan only is not a fact yet: it may be a file still being written.
func TestAnEditIsAFactOnlyOnceSettled(t *testing.T) {
	ctx := context.Background()
	_, b := pair(t, map[string]string{"x.pdf": "old"})
	tg, _ := tags.Parse("urgent")
	b.s.TagFiles(ctx, []int64{b.file("x.pdf")}, []tags.Tag{tg}, t0)
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
