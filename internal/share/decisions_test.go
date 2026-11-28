package share

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alansav/filo/internal/device"
	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

func (n *node) write(rel, content string) {
	n.t.Helper()
	p := filepath.Join(n.sync, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		n.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		n.t.Fatal(err)
	}
}

func (n *node) scan() {
	n.t.Helper()
	if _, err := reconcile.Scan(context.Background(), n.s, reconcile.Options{Target: n.root}); err != nil {
		n.t.Fatal(err)
	}
}

// file is the register's file at rel under the synced directory.
func (n *node) file(rel string) int64 {
	n.t.Helper()
	root, err := n.s.RootAt(context.Background(), n.root)
	if err != nil {
		n.t.Fatal(err)
	}
	f, err := n.s.FileAt(context.Background(), root.ID, "Sync/"+rel)
	if err != nil {
		n.t.Fatalf("%s: %s: %v", n.name, rel, err)
	}
	return f.FileID
}

func (n *node) values(rel string) store.FileValues {
	n.t.Helper()
	v, ok, err := n.s.Values(context.Background(), n.file(rel))
	if err != nil || !ok {
		n.t.Fatalf("%s: values of %s: %v", n.name, rel, err)
	}
	return v
}

func (n *node) set(rel, date, ret string, at time.Time) {
	n.t.Helper()
	sv := store.SetValues{Retention: ret, SetRetention: true}
	if date != "" {
		sv.FileDate, sv.SetFileDate = date, true
	}
	res, err := n.s.SetFiles(context.Background(), []int64{n.file(rel)}, sv, at, "2026-11-28",
		store.SetOptions{Confirm: func(store.SetResult) bool { return true }})
	if err != nil || len(res.Refusals) > 0 {
		n.t.Fatalf("%s: set %s: %v %+v", n.name, rel, err, res.Refusals)
	}
	n.publish(at)
}

func (n *node) publish(at time.Time) {
	n.t.Helper()
	problems, err := Publish(context.Background(), n.s, at)
	if err != nil || len(problems) > 0 {
		n.t.Fatalf("%s: publish: %v %q", n.name, err, problems)
	}
}

func (n *node) tags(rel string) []string {
	n.t.Helper()
	id := n.file(rel)
	m, err := n.s.FileTags(context.Background(), []int64{id})
	if err != nil {
		n.t.Fatal(err)
	}
	var out []string
	for _, t := range m[id] {
		out = append(out, t.String())
	}
	return out
}

// pair is two devices sharing Sync, each holding the files given (rel: content), scanned.
func pair(t *testing.T, files map[string]string) (a, b *node) {
	t.Helper()
	a, b = newNode(t, "laptop"), newNode(t, "desktop")
	for rel, c := range files {
		a.write(rel, c)
		b.write(rel, c)
	}
	a.scan()
	b.scan()
	a.mark()
	syncFilo(t, a, b)
	b.join()
	syncFilo(t, b, a)
	ctx := context.Background()
	a.imp(t0)
	kd, _ := FindDevice(ctx, a.s, "desktop")
	in, _ := Where(ctx, a.s, kd.Dev)
	if err := Trust(ctx, a.s, kd, in, t0); err != nil {
		t.Fatal(err)
	}
	a.imp(t0)
	syncFilo(t, a, b)
	b.imp(t0)
	return a, b
}

func TestRetentionAndTagsAreShared(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, map[string]string{"bills/gas.pdf": "gas"})
	a.set("bills/gas.pdf", "2026-01-01", "7y", t0.Add(time.Minute))
	syncFilo(t, a, b)
	dr := b.imp(t0.Add(2 * time.Minute))
	if v := b.values("bills/gas.pdf"); v.Retention != "7y" || v.FileDate != "2026-01-01" {
		t.Fatalf("b's file after importing a's decision: %+v (%+v)", v, dr)
	}
	if dr.Waiting != 0 || dr.Conflicts != 0 || len(dr.Problems) != 0 {
		t.Fatalf("b's import: %+v", dr)
	}
	// b's applying it is not written back to b's journal.
	before := b.lines(b.dev().ID)
	b.publish(t0.Add(3 * time.Minute))
	if b.lines(b.dev().ID) != before {
		t.Fatal("b wrote a's decision to its own journal")
	}
	// b tags the file; a gets the tag.
	tg, _ := tags.Parse("sender:British Gas")
	if _, err := b.s.TagFiles(ctx, []int64{b.file("bills/gas.pdf")}, []tags.Tag{tg}, t0.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	b.publish(t0.Add(4 * time.Minute))
	syncFilo(t, b, a)
	a.imp(t0.Add(5 * time.Minute))
	if got := a.tags("bills/gas.pdf"); len(got) != 1 || got[0] != "sender:British Gas" {
		t.Fatalf("a's tags = %q", got)
	}
}

func TestConflictTakesTheSafeSideUntilResolved(t *testing.T) {
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	a.set("x.pdf", "2026-01-01", "1y", t0.Add(time.Minute))
	b.set("x.pdf", "2026-01-01", "10y", t0.Add(time.Minute))
	syncFilo(t, a, b)
	syncFilo(t, b, a)
	da := a.imp(t0.Add(2 * time.Minute))
	db := b.imp(t0.Add(2 * time.Minute))
	if da.Conflicts != 1 || db.Conflicts != 1 {
		t.Fatalf("conflicts: a %d, b %d", da.Conflicts, db.Conflicts)
	}
	if va, vb := a.values("x.pdf"), b.values("x.pdf"); va.Retention != "10y" || vb.Retention != "10y" {
		t.Fatalf("the safe side: a %q, b %q", va.Retention, vb.Retention)
	}
	conf, _ := a.s.ConflictedFiles(context.Background())
	if len(conf[a.file("x.pdf")]) != 1 {
		t.Fatalf("ConflictedFiles = %v", conf)
	}
	// Any new decision resolves it everywhere.
	a.set("x.pdf", "", "5y", t0.Add(3*time.Minute))
	syncFilo(t, a, b)
	db = b.imp(t0.Add(4 * time.Minute))
	if v := b.values("x.pdf"); v.Retention != "5y" || db.Conflicts != 0 {
		t.Fatalf("after resolving: %+v, %d conflicts", v, db.Conflicts)
	}
	if da = a.imp(t0.Add(4 * time.Minute)); da.Conflicts != 0 {
		t.Fatalf("a still has %d conflicts", da.Conflicts)
	}
}

func TestEqualDecisionsMerge(t *testing.T) {
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	a.set("x.pdf", "2026-01-01", "7y", t0.Add(time.Minute))
	b.set("x.pdf", "2026-01-01", "7y", t0.Add(time.Minute))
	syncFilo(t, a, b)
	syncFilo(t, b, a)
	if da := a.imp(t0.Add(2 * time.Minute)); da.Conflicts != 0 {
		t.Fatalf("equal decisions are a conflict: %d", da.Conflicts)
	}
}

// The person's scenario: a decision on the desktop, the file then moved and renamed there; on the laptop the journal
// arrives first, or the file does.
func TestDecisionFollowsAMovedFile(t *testing.T) {
	for _, journalFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "journal first", false: "file first"}[journalFirst], func(t *testing.T) {
			a, b := pair(t, map[string]string{"bills/x.pdf": "x"})
			b.set("bills/x.pdf", "2026-01-01", "7y", t0.Add(time.Minute))
			// b moves it (the sync carries the move to a).
			move := func(n *node) {
				os.MkdirAll(filepath.Join(n.sync, "house"), 0o755)
				if err := os.Rename(filepath.Join(n.sync, "bills/x.pdf"), filepath.Join(n.sync, "house/gas.pdf")); err != nil {
					t.Fatal(err)
				}
				n.scan()
			}
			move(b)
			if journalFirst {
				syncFilo(t, b, a)
				a.imp(t0.Add(2 * time.Minute))
				move(a)
			} else {
				move(a)
				syncFilo(t, b, a)
				a.imp(t0.Add(2 * time.Minute))
			}
			a.imp(t0.Add(3 * time.Minute))
			if v := a.values("house/gas.pdf"); v.Retention != "7y" {
				t.Fatalf("a's moved file: %+v", v)
			}
		})
	}
}

// A decision about a file the sync has not brought yet waits, and applies once the file is here.
func TestDecisionWaitsForItsFile(t *testing.T) {
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	a.write("new.pdf", "new")
	a.scan()
	a.set("new.pdf", "2026-02-01", "3y", t0.Add(time.Minute))
	syncFilo(t, a, b)
	if dr := b.imp(t0.Add(2 * time.Minute)); dr.Waiting != 2 {
		t.Fatalf("waiting = %d, want 2 (its date and retention)", dr.Waiting)
	}
	b.write("new.pdf", "new")
	b.scan()
	dr := b.imp(t0.Add(3 * time.Minute))
	if v := b.values("new.pdf"); v.Retention != "3y" || dr.Waiting != 0 {
		t.Fatalf("after the file arrived: %+v, waiting %d", v, dr.Waiting)
	}
}

// A retention decided on a content applies only to that content: an older version here waits for the new one.
func TestRetentionWaitsForItsContent(t *testing.T) {
	a, b := pair(t, map[string]string{"x.pdf": "old"})
	a.write("x.pdf", "new")
	a.scan()
	a.set("x.pdf", "2026-01-01", "7y", t0.Add(time.Minute))
	syncFilo(t, a, b)
	if dr := b.imp(t0.Add(2 * time.Minute)); dr.Waiting == 0 || b.values("x.pdf").Retention != "" {
		t.Fatalf("an old version took a retention decided on the new: %+v", dr)
	}
	b.write("x.pdf", "new")
	b.scan()
	b.imp(t0.Add(3 * time.Minute))
	if v := b.values("x.pdf"); v.Retention != "7y" {
		t.Fatalf("after the new version arrived: %+v", v)
	}
}

// A decision about a file outside the synced directory is never written to a journal.
func TestDecisionsOutsideAreNotShared(t *testing.T) {
	ctx := context.Background()
	a, _ := pair(t, map[string]string{"x.pdf": "x"})
	p := filepath.Join(a.root, "private.pdf")
	os.WriteFile(p, []byte("p"), 0o644)
	a.scan()
	root, _ := a.s.RootAt(ctx, a.root)
	f, err := a.s.FileAt(ctx, root.ID, "private.pdf")
	if err != nil {
		t.Fatal(err)
	}
	before := a.lines(a.dev().ID)
	if _, err := a.s.SetFiles(ctx, []int64{f.FileID}, store.SetValues{Retention: "forever", SetRetention: true}, t0,
		"2026-11-28", store.SetOptions{}); err != nil {
		t.Fatal(err)
	}
	a.publish(t0)
	if a.lines(a.dev().ID) != before {
		t.Fatal("a decision about a file outside the synced directory was written to the journal")
	}
}

func TestEffective(t *testing.T) {
	ds := func(values ...string) []store.Decision {
		var out []store.Decision
		for _, v := range values {
			out = append(out, store.Decision{Value: v})
		}
		return out
	}
	// keep-until of a retention given a file_date of 2026-01-01; a date value as itself plus 1y.
	until := func(key, v string) string {
		if key == keyDate {
			return v
		}
		switch v {
		case "1y":
			return "2027-01-01"
		case "10y":
			return "2036-01-01"
		case "forever":
			return store.Forever
		case "nopolicy":
			return ""
		}
		return v
	}
	tests := []struct {
		name, key string
		values    []string
		want      string
		conflict  bool
	}{
		{"one", keyRetention, []string{"7y"}, "7y", false},
		{"equal", keyRetention, []string{"7y", "7y"}, "7y", false},
		{"the longer", keyRetention, []string{"1y", "10y"}, "10y", true},
		{"forever is longest", keyRetention, []string{"10y", "forever"}, "forever", true},
		{"none wins", keyRetention, []string{"10y", ""}, "", true},
		{"not worked out: none", keyRetention, []string{"10y", "nopolicy"}, "", true},
		{"the later date", keyDate, []string{"2026-01-01", "2026-06-01"}, "2026-06-01", true},
		{"no date wins", keyDate, []string{"2026-01-01", ""}, "", true},
		{"a tag added wins", "tag:x:y", []string{"", "x:Y"}, "x:Y", true},
	}
	for _, tt := range tests {
		got, conflict := effective(tt.key, ds(tt.values...), until)
		if got != tt.want || conflict != tt.conflict {
			t.Errorf("%s: effective = %q, %v; want %q, %v", tt.name, got, conflict, tt.want, tt.conflict)
		}
	}
}

func TestDerivedID(t *testing.T) {
	a := derivedID("sync", "hash", "bills/x.pdf")
	if !device.ValidID(a) || a != derivedID("sync", "hash", "bills/x.pdf") {
		t.Fatalf("derivedID = %q: not a valid, stable ID", a)
	}
	if a == derivedID("sync", "hash", "bills/y.pdf") || a == derivedID("other", "hash", "bills/x.pdf") {
		t.Fatal("derivedID does not depend on the path and the synced directory")
	}
}
