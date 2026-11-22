package reconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/tags"
)

// Tags (docs/design.md, Tags): what a scan does with them.

func (e *env) scan() Summary {
	e.t.Helper()
	sum, err := Scan(context.Background(), e.s, Options{Target: e.base})
	if err != nil {
		e.t.Fatal(err)
	}
	return sum
}

func (e *env) tag(rel string, ts ...string) {
	e.t.Helper()
	var add []tags.Tag
	for _, s := range ts {
		t, err := tags.Parse(s)
		if err != nil {
			e.t.Fatal(err)
		}
		add = append(add, t)
	}
	if _, err := e.s.TagFiles(context.Background(), []int64{e.known()[rel].FileID}, add, time.Now()); err != nil {
		e.t.Fatal(err)
	}
}

// tagsOf is the tags of the file at rel, joined with "; ".
func (e *env) tagsOf(rel string) string {
	e.t.Helper()
	id := e.known()[rel].FileID
	m, err := e.s.FileTags(context.Background(), []int64{id})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, t := range m[id] {
		out = append(out, t.String())
	}
	return strings.Join(out, "; ")
}

func TestScanCopiesTags(t *testing.T) {
	tests := []struct {
		name     string
		do       func(e *env)
		rel      string // the new file
		want     string // its tags
		differed int
	}{
		{"a copy of a tagged file takes its tags, quietly", func(e *env) {
			e.write("copy.pdf", "bill")
		}, "copy.pdf", "sender:British Gas; type:bill", 0},
		{"copies whose tags differ give their union, and the scan says so", func(e *env) {
			e.write("b.pdf", "bill")
			e.scan()
			e.tag("b.pdf", "project:house")
			e.write("copy.pdf", "bill")
		}, "copy.pdf", "project:house; sender:British Gas; type:bill", 1},
		{"an untagged copy beside a tagged one differs too", func(e *env) {
			e.write("b.pdf", "bill")
			e.scan() // b takes a's tags, then loses them
			if _, err := e.s.UntagFiles(context.Background(), []int64{e.known()["b.pdf"].FileID},
				[]tags.Search{{Key: "type", AnyValue: true}, {Key: "sender", AnyValue: true}}, time.Now()); err != nil {
				e.t.Fatal(err)
			}
			e.write("copy.pdf", "bill")
		}, "copy.pdf", "sender:British Gas; type:bill", 1},
		{"another content takes nothing", func(e *env) {
			e.write("other.pdf", "letter")
		}, "other.pdf", "", 0},
		{"a file edited in this scan is not a copy of what it held", func(e *env) {
			e.write("copy.pdf", "bill")
			e.write("a.pdf", "edited")
		}, "copy.pdf", "", 0},
		{"a moved file keeps its own tags, and gives none", func(e *env) {
			e.mv("a.pdf", "moved.pdf")
		}, "moved.pdf", "sender:British Gas; type:bill", 0},
		{"a file this scan finds missing gives none, even when the move is not paired", func(e *env) {
			e.write("b/a.pdf", "bill")
			e.write("c/a.pdf", "bill")
			e.rm("a.pdf") // two places it may have gone: a scan never guesses which
		}, "b/a.pdf", "", 0},
		{"an empty file is no copy", func(e *env) {
			e.write("empty1", "")
			e.scan()
			e.tag("empty1", "urgent")
			e.write("empty2", "")
		}, "empty2", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, nil)
			e.write("a.pdf", "bill")
			e.scan()
			e.tag("a.pdf", "type:bill", "sender:British Gas")
			tt.do(e)
			sum := e.scan()
			if got := e.tagsOf(tt.rel); got != tt.want {
				t.Errorf("tags of %s = %q, want %q", tt.rel, got, tt.want)
			}
			if sum.CopyTags != tt.differed || len(sum.Samples[CopyTags]) != tt.differed {
				t.Errorf("CopyTags = %d (%v), want %d", sum.CopyTags, sum.Samples[CopyTags], tt.differed)
			}
			if tt.differed > 0 {
				ev := sum.Samples[CopyTags][0]
				if ev.Path != tt.rel || ev.Detail != "took the tags of copies that differed: "+tt.want {
					t.Errorf("event = %+v", ev)
				}
			}
			if _, ok := e.known()["a.pdf"]; ok && !strings.Contains(e.tagsOf("a.pdf"), "type:bill") {
				t.Errorf("the original's tags = %q", e.tagsOf("a.pdf"))
			}
		})
	}
}

// TestPossibleMoveNotesTags: a file saved by writing a new one and renaming it into place stays a possible move, and
// its event says the old file had tags, which are not carried over.
func TestPossibleMoveNotesTags(t *testing.T) {
	e := newEnv(t, nil)
	e.write("old/report.txt", "draft")
	e.write("plain/notes.txt", "a")
	e.scan()
	e.tag("old/report.txt", "project:x")
	e.mv("old/report.txt", "new/report.txt")
	e.appendTo("new/report.txt", " edited")
	e.rm("plain/notes.txt")
	e.write("plain2/notes.txt", "b")
	sum := e.scan()
	details := map[string]string{}
	for _, ev := range sum.Samples[PossibleMove] {
		details[ev.Path] = ev.Detail
	}
	if details["new/report.txt"] != "same name, different content; the old file had tags" ||
		details["plain2/notes.txt"] != "same name, different content" {
		t.Errorf("possible moves = %q", details)
	}
	if got := e.tagsOf("new/report.txt"); got != "" {
		t.Errorf("tags of the new file = %q, want none", got)
	}
}
