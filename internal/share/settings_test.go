package share

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
)

func (n *node) rootID() int64 {
	n.t.Helper()
	r, err := n.s.RootAt(context.Background(), n.root)
	if err != nil {
		n.t.Fatal(err)
	}
	return r.ID
}

func (n *node) policy(name string) (store.Policy, bool) {
	p, err := n.s.Policy(context.Background(), name)
	return p, err == nil
}

func TestPoliciesAreShared(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	if err := a.s.AddPolicy(ctx, "bills", "7y", t0); err != nil {
		t.Fatal(err)
	}
	a.publish(t0)
	syncFilo(t, a, b)
	b.imp(t0.Add(time.Minute))
	if p, ok := b.policy("bills"); !ok || p.Value != "7y" {
		t.Fatalf("b's policy bills: %+v, %v", p, ok)
	}
	if _, err := a.s.SetPolicy(ctx, "bills", "10y", t0); err != nil {
		t.Fatal(err)
	}
	a.publish(t0.Add(2 * time.Minute))
	syncFilo(t, a, b)
	b.imp(t0.Add(3 * time.Minute))
	if p, _ := b.policy("bills"); p.Value != "10y" {
		t.Fatalf("b's policy after a set it: %+v", p)
	}
	// Applying a's policy does not come back as b's own.
	before := b.lines(b.dev().ID)
	b.publish(t0.Add(4 * time.Minute))
	if b.lines(b.dev().ID) != before {
		t.Fatal("b wrote a's policy to its journal")
	}
	// A rename reaches b as a rename: b's files on it follow.
	b.set("x.pdf", "2026-01-01", "bills", t0.Add(5*time.Minute))
	syncFilo(t, b, a)
	a.imp(t0.Add(6 * time.Minute))
	if _, err := a.s.MovePolicy(ctx, "bills", "taxes", t0); err != nil {
		t.Fatal(err)
	}
	a.publish(t0.Add(7 * time.Minute))
	syncFilo(t, a, b)
	b.imp(t0.Add(8 * time.Minute))
	if _, ok := b.policy("bills"); ok {
		t.Fatal("b still has the policy renamed on a")
	}
	if p, ok := b.policy("taxes"); !ok || p.Value != "10y" {
		t.Fatalf("b's renamed policy: %+v, %v", p, ok)
	}
	if v := b.values("x.pdf"); v.Retention != "taxes" {
		t.Fatalf("b's file on the renamed policy: %+v", v)
	}
}

func TestRulesExclusionsAlwaysAndIndexesAreShared(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, map[string]string{"bills/x.pdf": "x", "c1/same.pdf": "same", "c2/same.pdf": "same"})
	if _, _, _, err := a.s.AddRule(ctx, a.rootID(), "Sync/bills", "*", "bills", "2026-11-28", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := a.s.Exclude(ctx, a.rootID(), "Sync/c1", t0); err != nil {
		t.Fatal(err)
	}
	vals := a.values("c2/same.pdf")
	if err := a.s.AddAlways(ctx, vals.Hash, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := a.s.AddTagIndex(ctx, a.rootID(), "Sync", "index", t0); err != nil {
		t.Fatal(err)
	}
	a.publish(t0)
	syncFilo(t, a, b)
	if dr := b.imp(t0.Add(time.Minute)); len(dr.Problems) != 0 || dr.Waiting != 0 {
		t.Fatalf("b's import: %+v", dr)
	}
	if rs, _ := b.s.Rules(ctx, b.rootID(), "Sync"); len(rs) != 1 || rs[0].Dir != "Sync/bills" || rs[0].Policy != "bills" {
		t.Fatalf("b's rules: %+v", rs)
	}
	if _, ok, _ := b.s.Exclusion(ctx, b.rootID(), "Sync/c1"); !ok {
		t.Fatal("b's Sync/c1 is not excluded")
	}
	if hs, _ := b.s.AlwaysHashes(ctx); len(hs) != 1 || hs[0] != vals.Hash {
		t.Fatalf("b's always: %q", hs)
	}
	ixs, _ := b.s.TagIndexes(ctx)
	if len(ixs) != 1 || ixs[0].Writer != a.dev().ID {
		t.Fatalf("b's index: %+v", ixs)
	}
	// b writes nothing back for what it took in.
	before := b.lines(b.dev().ID)
	b.publish(t0.Add(2 * time.Minute))
	if b.lines(b.dev().ID) != before {
		t.Fatal("b wrote back the settings it took in")
	}
	// The rule removed on a is removed on b.
	if _, err := a.s.RemoveRule(ctx, a.rootID(), "Sync/bills", "*"); err != nil {
		t.Fatal(err)
	}
	a.publish(t0.Add(3 * time.Minute))
	syncFilo(t, a, b)
	b.imp(t0.Add(4 * time.Minute))
	if rs, _ := b.s.Rules(ctx, b.rootID(), "Sync"); len(rs) != 0 {
		t.Fatalf("b's rules after the removal: %+v", rs)
	}
}

func TestPolicyConflictKeepsTheLonger(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	a.s.AddPolicy(ctx, "bills", "1y", t0)
	b.s.AddPolicy(ctx, "bills", "10y", t0)
	a.publish(t0)
	b.publish(t0)
	syncFilo(t, a, b)
	syncFilo(t, b, a)
	a.imp(t0.Add(time.Minute))
	b.imp(t0.Add(time.Minute))
	pa, _ := a.policy("bills")
	pb, _ := b.policy("bills")
	if pa.Value != "10y" || pb.Value != "10y" {
		t.Fatalf("policy in conflict: a %q, b %q", pa.Value, pb.Value)
	}
}

// churn runs a change on both devices (so each publishes its settings) and syncs both ways, a few times.
func churn(t *testing.T, a, b *node, rounds int) {
	t.Helper()
	ctx := context.Background()
	for i := range rounds {
		at := t0.Add(time.Duration(10+i) * time.Minute)
		for _, n := range []*node{a, b} {
			n.s.AddPolicy(ctx, fmt.Sprintf("churn%s%d", n.name, at.Unix()), "1y", at) // a change: the run publishes
			n.publish(at)
		}
		syncFilo(t, a, b)
		syncFilo(t, b, a)
		a.imp(at)
		b.imp(at)
	}
}

func TestRuleConflictStaysUntilDecided(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	a.s.AddRule(ctx, a.rootID(), "Sync", "*.pdf", "keep", "2026-11-28", t0)
	b.s.AddRule(ctx, b.rootID(), "Sync", "*.pdf", "short", "2026-11-28", t0)
	a.publish(t0)
	b.publish(t0)
	syncFilo(t, a, b)
	syncFilo(t, b, a)
	a.imp(t0.Add(time.Minute))
	b.imp(t0.Add(time.Minute))
	churn(t, a, b, 2)
	// The rule's conflict stays (and so does the one on the file it dated otherwise on each device).
	keys, _ := a.s.Conflicts(ctx, mustDir(t, a).ID)
	found := false
	for _, k := range keys {
		found = found || k[1] == "rule://*.pdf"
	}
	if !found {
		t.Fatalf("a rule conflict ended by itself: conflicts %v", keys)
	}
	if rs, _ := a.s.Rules(ctx, a.rootID(), "Sync"); len(rs) != 0 {
		t.Fatalf("the safe side of a rule conflict is no rule: %+v", rs)
	}
}

func TestRulePatternCaseDoesNotChurn(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	a.s.AddRule(ctx, a.rootID(), "Sync", "*.pdf", "keep", "2026-11-28", t0)
	a.publish(t0)
	syncFilo(t, a, b)
	b.imp(t0)
	b.s.RemoveRule(ctx, b.rootID(), "Sync", "*.pdf")
	b.s.AddRule(ctx, b.rootID(), "Sync", "*.PDF", "keep", "2026-11-28", t0)
	b.publish(t0)
	syncFilo(t, b, a)
	a.imp(t0)
	churn(t, a, b, 1)
	la, lb := a.lines(a.dev().ID), b.lines(b.dev().ID)
	churn(t, a, b, 3)
	// Each round adds at most one policy line per device (the churn itself), nothing more.
	if a.lines(a.dev().ID) > la+3 || b.lines(b.dev().ID) > lb+3 {
		t.Fatalf("lines grew: a %d -> %d, b %d -> %d", la, a.lines(a.dev().ID), lb, b.lines(b.dev().ID))
	}
}

func TestForgettingAnIndexStaysHere(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	a.s.AddTagIndex(ctx, a.rootID(), "Sync", "index", t0)
	a.publish(t0)
	syncFilo(t, a, b)
	b.imp(t0)
	ixs, _ := b.s.TagIndexes(ctx)
	if len(ixs) != 1 {
		t.Fatalf("b's indexes: %+v", ixs)
	}
	b.s.RemoveTagIndex(ctx, ixs[0].ID)
	churn(t, a, b, 2)
	if ixs, _ := b.s.TagIndexes(ctx); len(ixs) != 0 {
		t.Fatalf("a forgotten index came back on b: %+v", ixs)
	}
	if ixs, _ := a.s.TagIndexes(ctx); len(ixs) != 1 {
		t.Fatalf("a lost the index b forgot: %+v", ixs)
	}
}

func TestABadPolicyValueIsAProblem(t *testing.T) {
	a, b := pair(t, map[string]string{"x.pdf": "x"})
	kept := mustLines(t, a, a.dev().ID)
	appendLine(t, a, kept, KindSetting, SettingBody{Key: "policy:odd", Value: "6q"})
	syncFilo(t, a, b)
	dr := b.imp(t0)
	if len(dr.Problems) != 1 {
		t.Fatalf("a bad policy value: %+v", dr.Problems)
	}
}

// appendLine writes one line after kept to n's journal on the disk, as n would.
func appendLine(t *testing.T, n *node, kept []store.JournalLine, kind string, body any) {
	t.Helper()
	last, _ := journal.Parse(kept[len(kept)-1].Raw)
	w, _ := journal.Resume(n.dev().Key, last)
	raw, err := w.Add(kind, body, t0)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(filepath.Join(n.sync, journal.FiloDir, journal.JournalName(n.dev().ID)), os.O_APPEND|os.O_WRONLY, 0)
	f.Write(append(raw, '\n'))
	f.Close()
}
