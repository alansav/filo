package share

import (
	"context"
	"testing"
	"time"

	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

// Decisions made before a directory was marked or joined are seeded: all of them on marking, and on joining only those
// that differ from what is in effect (conflicts), equal ones not written again.
func TestSeeding(t *testing.T) {
	ctx := context.Background()
	a, b := newNode(t, "laptop"), newNode(t, "desktop")
	for _, n := range []*node{a, b} {
		n.write("bills/gas.pdf", "gas")
		n.write("x.pdf", "x")
		n.scan()
	}
	setOn := func(n *node, rel, ret string) {
		if _, err := n.s.SetFiles(ctx, []int64{n.file(rel)}, store.SetValues{Retention: ret, SetRetention: true,
			FileDate: "2026-01-01", SetFileDate: true}, t0, "2026-11-28", store.SetOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	setOn(a, "bills/gas.pdf", "7y")
	setOn(a, "x.pdf", "1y")
	tg, _ := tags.Parse("sender:EDF")
	if _, err := a.s.TagFiles(ctx, []int64{a.file("bills/gas.pdf")}, []tags.Tag{tg}, t0); err != nil {
		t.Fatal(err)
	}
	setOn(b, "bills/gas.pdf", "7y") // the same as a: no conflict
	setOn(b, "x.pdf", "10y")        // otherwise: a conflict
	for _, n := range []*node{a, b} {
		problems, err := Publish(ctx, n.s, t0) // decisions made before any synced directory: nothing written
		if err != nil || len(problems) > 0 {
			t.Fatal(err, problems)
		}
	}
	a.mark()
	syncFilo(t, a, b)
	_, notes := b.join()
	if len(notes) > 0 {
		t.Fatalf("join notes: %q", notes)
	}
	syncFilo(t, b, a)
	a.imp(t0.Add(time.Minute))
	kd, _ := FindDevice(ctx, a.s, "desktop")
	in, _ := Where(ctx, a.s, kd.Dev)
	if err := Trust(ctx, a.s, kd, in, t0); err != nil {
		t.Fatal(err)
	}
	da := a.imp(t0.Add(2 * time.Minute))
	syncFilo(t, a, b)
	db := b.imp(t0.Add(3 * time.Minute))
	if got := b.tags("bills/gas.pdf"); len(got) != 1 {
		t.Fatalf("b did not get a's seeded tag: %q", got)
	}
	if da.Conflicts != 1 || db.Conflicts != 1 {
		t.Fatalf("conflicts after seeding: a %d, b %d (want x.pdf's retention only)", da.Conflicts, db.Conflicts)
	}
	if v := b.values("x.pdf"); v.Retention != "10y" {
		t.Fatalf("the safe side of the seeded conflict: %+v", v)
	}
	// b wrote only what differed: x.pdf's retention, not gas.pdf's equal retention or its date.
	for _, l := range mustLines(t, b, b.dev().ID) {
		if d, _, err := readDecision(0, mustParse(t, l.Raw)); err == nil && d.Key == keyRetention && d.Value == "7y" {
			t.Fatal("b seeded a retention equal to the one in effect")
		}
	}
}

func mustParse(t *testing.T, raw []byte) journal.Line {
	t.Helper()
	l, err := journal.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
