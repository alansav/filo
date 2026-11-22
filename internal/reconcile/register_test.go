package reconcile

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func (e *env) prepare(rel string) (*Pending, string) {
	e.t.Helper()
	p, why, err := PrepareFile(context.Background(), e.s, e.root, rel)
	if err != nil {
		e.t.Fatal(err)
	}
	return p, why
}

// TestRegisterFile: a file filo tag registers by itself is a new file, in a scan whose scope is the file, with its
// copies' tags; the next scan treats it as known.
func TestRegisterFile(t *testing.T) {
	e := newEnv(t, nil)
	e.write("received/a.pdf", "bill")
	e.write("received/other.txt", "x")
	e.scan()
	e.tag("received/a.pdf", "type:bill")
	e.write("inbox/copy.pdf", "bill")
	e.write("inbox/unseen.txt", "y")

	p, why := e.prepare("inbox/copy.pdf")
	if why != "" {
		t.Fatalf("refused: %s", why)
	}
	if got := e.rows()["files"]; got != 2 {
		t.Errorf("PrepareFile wrote: %d files", got)
	}
	sum, err := p.Register(context.Background(), e.s)
	if err != nil {
		t.Fatal(err)
	}
	k := e.known()
	if _, ok := k["inbox/copy.pdf"]; !ok || sum.New != 1 || sum.Scope != "inbox/copy.pdf" {
		t.Fatalf("registered: %+v; known %v", sum, k)
	}
	if _, ok := k["inbox/unseen.txt"]; ok {
		t.Error("its directory was scanned")
	}
	if got := e.tagsOf("inbox/copy.pdf"); got != "type:bill" {
		t.Errorf("tags = %q", got)
	}
	si, ok, err := e.s.Scan(context.Background(), sum.ScanID)
	if err != nil || !ok || si.Scope != "inbox/copy.pdf" || si.FilesSeen != 1 || si.New != 1 || !si.Finished {
		t.Errorf("scan record = %+v, %v, %v", si, ok, err)
	}
	next := e.scan()
	if next.New != 1 || next.Moved+next.Missing+next.Changed != 0 { // only unseen.txt
		t.Errorf("next scan: %+v", countsOf(next))
	}
}

func TestPrepareFileRefuses(t *testing.T) {
	tests := []struct {
		name string
		do   func(e *env)
		rel  string
		want string // the start of why
	}{
		{"hidden", func(e *env) { e.write(".hidden/a.txt", "a") }, ".hidden/a.txt", "its root's settings skip it"},
		{"not on the disk", func(e *env) {}, "nothing.txt", "not on the disk"},
		{"a symlink", func(e *env) { e.symlink("kept.txt", "link") }, "link", "a symlink"},
		{"moved: the same inode, its old path gone", func(e *env) { e.mv("kept.txt", "inbox/kept.txt") },
			"inbox/kept.txt", "looks moved from "},
		{"moved: the same content, its old path gone", func(e *env) {
			e.rm("kept.txt")
			e.write("inbox/kept.txt", "kept content")
		}, "inbox/kept.txt", "looks moved from "},
		{"the content of a file missing since an earlier scan", func(e *env) {
			e.rm("kept.txt")
			e.scan()
			e.write("inbox/back.txt", "kept content")
		}, "inbox/back.txt", "looks moved from "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, nil)
			e.write("kept.txt", "kept content")
			e.scan()
			tt.do(e)
			p, why := e.prepare(tt.rel)
			if p != nil || !strings.HasPrefix(why, tt.want) {
				t.Errorf("PrepareFile(%s) = %v, %q; want %q", tt.rel, p, why, tt.want)
			}
			if strings.HasPrefix(tt.want, "looks moved") && !strings.Contains(why, filepath.Join(e.base, "kept.txt")) {
				t.Errorf("why = %q, want the old path", why)
			}
		})
	}
}

// TestPrepareFileHardLinkAndCopy: a hard link to a known file, and a copy whose original is still there, are new
// files, not moves.
func TestPrepareFileHardLinkAndCopy(t *testing.T) {
	e := newEnv(t, nil)
	e.write("kept.txt", "kept content")
	e.scan()
	e.write("inbox/copy.txt", "kept content")
	e.link("kept.txt", "inbox/link.txt")
	for _, rel := range []string{"inbox/link.txt", "inbox/copy.txt"} {
		if p, why := e.prepare(rel); p == nil {
			t.Errorf("PrepareFile(%s) refused: %s", rel, why)
		}
	}
}
