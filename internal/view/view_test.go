package view

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"testing"
)

// plainView renders into a buffer as when piped: no colour, no width.
func plainView() (*Renderer, *bytes.Buffer) {
	var b bytes.Buffer
	return New(Options{Out: &b, Home: "/home/u"}), &b
}

// termView renders into a buffer as in a terminal of the given width, with or without colour.
func termView(width int, color bool) (*Renderer, *bytes.Buffer) {
	var b bytes.Buffer
	return New(Options{Out: &b, Terminal: true, Color: color, Width: width, Home: "/home/u"}), &b
}

func TestHeading(t *testing.T) {
	tests := []struct {
		name  string
		where Where
		want  string
	}{
		{"one root", Where{Roots: []string{"/home/u/Documents"}}, "Ready   5 files . ~/Documents\n"},
		{"under", Where{Roots: []string{"/home/u/Documents", "/home/u/Documents"}, Under: "video"},
			"Ready   5 files . ~/Documents, under video/\n"},
		{"several roots", Where{Roots: []string{"/home/u/Documents", "/media/archive"}}, "Ready   5 files . 2 roots\n"},
		{"home itself", Where{Roots: []string{"/home/u"}}, "Ready   5 files . ~\n"},
		{"outside home", Where{Roots: []string{"/home/user2/x"}}, "Ready   5 files . /home/user2/x\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, b := plainView()
			r.Heading("Ready", tt.where, "5 files")
			if b.String() != tt.want {
				t.Errorf("got %q, want %q", b.String(), tt.want)
			}
		})
	}
}

func TestSection(t *testing.T) {
	tests := []struct {
		name  string
		width int
		want  string
	}{
		{"piped", 0, "Past   5 files . 820 MB\n"},
		{"one column short of the right edge", 30, "Past         5 files · 820 MB\n"},
		{"wider than the terminal", 10, "Past   5 files · 820 MB\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r *Renderer
			var b *bytes.Buffer
			if tt.width == 0 {
				r, b = plainView()
			} else {
				r, b = termView(tt.width, false)
				r.o.Color = false // the width without the colour codes, so the line can be compared
			}
			r.Section("Past", "5 files", "820 MB")
			got := b.String()
			if tt.width > 0 {
				got = strings.ReplaceAll(got, " . ", " · ")
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFiles(t *testing.T) {
	cols := []Column{{Header: "until"}, {Header: "size", Right: true}}
	rows := func(rels ...string) []FileRow {
		var out []FileRow
		for i, rel := range rels {
			size := "3 KB"
			if i == 1 {
				size = "640 MB"
			}
			out = append(out, FileRow{Root: "/home/u/Documents", Rel: rel, Cells: []string{"2025-06-30", size}})
		}
		return out
	}
	tests := []struct {
		name  string
		width int
		list  FileList
		roots []string
		want  string
	}{
		{"aligned", 0, FileList{Columns: cols, Rows: rows("notes/a.txt", "video/b.mp4")},
			[]string{"/home/u/Documents"}, "" +
				"    until          size\n" +
				"    2025-06-30     3 KB   notes/a.txt\n" +
				"    2025-06-30   640 MB   video/b.mp4\n"},
		{"cut after Max", 0, FileList{Columns: cols, Rows: rows("a", "b", "c"), Max: 2, More: "files"},
			[]string{"/home/u/Documents"}, "" +
				"    until          size\n" +
				"    2025-06-30     3 KB   a\n" +
				"    2025-06-30   640 MB   b\n" +
				"    ... 1 more files\n"},
		{"a long path goes on its own line", 40, FileList{Columns: cols,
			Rows: rows("short.txt", "video/2015/summer holiday, day 3.mp4")},
			[]string{"/home/u/Documents"}, "" +
				"    until          size\n" +
				"    2025-06-30     3 KB   short.txt\n" +
				"    2025-06-30   640 MB\n" +
				"      video/2015/summer holiday, day 3.mp4\n"},
		{"piped never wraps", 0, FileList{Columns: cols, Rows: rows("x", "video/2015/summer holiday, day 3.mp4")},
			[]string{"/home/u/Documents"}, "" +
				"    until          size\n" +
				"    2025-06-30     3 KB   x\n" +
				"    2025-06-30   640 MB   video/2015/summer holiday, day 3.mp4\n"},
		{"several roots", 0, FileList{Columns: []Column{{}}, ByRoot: true, Rows: []FileRow{
			{Root: "/home/u/Documents", Rel: "a", Cells: []string{"1"}},
			{Root: "/media/archive", Rel: "b", Cells: []string{"2"}}}},
			[]string{"/home/u/Documents", "/media/archive"}, "" +
				"  ~/Documents\n" +
				"    1   a\n" +
				"  /media/archive\n" +
				"    2   b\n"},
		{"names escaped", 0, FileList{Rows: []FileRow{{Root: "/home/u/Documents", Rel: "evil\x1b[2K\n.txt"}}},
			[]string{"/home/u/Documents"}, "    evil\\x1b[2K\\n.txt\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r *Renderer
			var b *bytes.Buffer
			if tt.width == 0 {
				r, b = plainView()
			} else {
				r, b = termView(tt.width, false)
			}
			r.Heading("H", Where{Roots: tt.roots})
			b.Reset()
			r.Files(tt.list)
			if b.String() != tt.want {
				t.Errorf("got\n%s\nwant\n%s", b.String(), tt.want)
			}
		})
	}
}

func TestDup(t *testing.T) {
	tests := []struct {
		name  string
		group DupGroup
		want  string
	}{
		{"in the heading's root", DupGroup{Name: "contract.pdf", Each: "8 MB",
			Keep:   []Copy{{Root: "/home/u/Documents", Rel: "a/contract.pdf", Note: "forever"}},
			Delete: []Copy{{Root: "/home/u/Documents", Rel: "b/contract.pdf", Symlinks: []string{"/home/u/Desktop/c"}}}},
			"" +
				"    contract.pdf . 8 MB each\n" +
				"      keep     a/contract.pdf   forever\n" +
				"      delete   b/contract.pdf\n" +
				"               -> the symlink ~/Desktop/c points here and would break\n"},
		{"spanning roots: full paths", DupGroup{Name: "m", Each: "1 B",
			Keep:   []Copy{{Root: "/home/u/Documents", Rel: "m"}},
			Delete: []Copy{{Root: "/media/archive", Rel: "old/m"}}},
			"" +
				"    m . 1 B each\n" +
				"      keep     ~/Documents/m\n" +
				"      delete   /media/archive/old/m\n"},
		{"no copy kept", DupGroup{Name: "m", Each: "1 B", NoKeep: "no other copy is left",
			Delete: []Copy{{Root: "/home/u/Documents", Rel: "m"}}},
			"" +
				"    m . 1 B each\n" +
				"      keep     no other copy is left\n" +
				"      delete   m\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, b := plainView()
			r.Heading("H", Where{Roots: []string{"/home/u/Documents"}})
			b.Reset()
			r.Dup(tt.group)
			if b.String() != tt.want {
				t.Errorf("got\n%s\nwant\n%s", b.String(), tt.want)
			}
		})
	}
}

func TestProblems(t *testing.T) {
	many := func(n int) []Problem {
		var ps []Problem
		for i := range n {
			ps = append(ps, Problem{Line: i + 2, Root: "/home/u/Documents", Rel: "f", Reason: "in the wrong group"})
		}
		return ps
	}
	tests := []struct {
		name string
		ps   []Problem
		want string
	}{
		{"one by one", []Problem{
			{Line: 7, Root: "/home/u/Documents", Rel: "invoices/2019.pdf", Reason: "retention 7y needs a file_date"},
			{Line: 31, Root: "/home/u/Documents", Rel: "notes/old.txt", Reason: "changed since the export"}},
			"" +
				"Refused\n" +
				"  [refused] line 7    invoices/2019.pdf: retention 7y needs a file_date\n" +
				"  [refused] line 31   notes/old.txt: changed since the export\n"},
		{"ten with one reason stay listed", many(10), ""},
		{"more than ten collapse", append(many(11), Problem{Line: 40, Root: "/home/u/Documents", Rel: "g",
			Reason: "other"}), "" +
			"Refused\n" +
			"  [refused] lines 2-12   11 rows   in the wrong group\n" +
			"  [refused] line 40   g: other\n"},
		{"no line or group", []Problem{{Root: "/home/u/Documents", Rel: "tax/2017.pdf", Reason: "changed"}},
			"Refused\n  [refused] tax/2017.pdf: changed\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, b := plainView()
			r.Heading("H", Where{Roots: []string{"/home/u/Documents"}})
			b.Reset()
			r.Problems("Refused", Refused, tt.ps)
			if tt.want == "" {
				if n := strings.Count(b.String(), "\n"); n != len(tt.ps)+1 {
					t.Errorf("%d lines, want %d:\n%s", n, len(tt.ps)+1, b.String())
				}
				return
			}
			if b.String() != tt.want {
				t.Errorf("got\n%s\nwant\n%s", b.String(), tt.want)
			}
		})
	}
}

func TestPick(t *testing.T) {
	choices := []Choice{{Key: "a", Word: "all"}, {Key: "s", Word: "select"}, {Key: "n", Word: "none"}}
	tests := []struct {
		answer, key string
		ok          bool
	}{
		{"a", "a", true},
		{"ALL", "a", true},
		{"  select ", "s", true},
		{"", "n", true},
		{"al", "", false},
		{"delete this", "", false},
	}
	for _, tt := range tests {
		key, ok := Pick(tt.answer, choices, "n")
		if key != tt.key || ok != tt.ok {
			t.Errorf("Pick(%q) = %q, %v; want %q, %v", tt.answer, key, ok, tt.key, tt.ok)
		}
	}
}

func TestPrompts(t *testing.T) {
	choices := []Choice{{Key: "a", Word: "all", Label: "do all"}, {Key: "s", Word: "select", Label: "select"},
		{Key: "n", Word: "none", Label: "none"}}
	r, b := plainView()
	r.Ask("Delete all 5?", choices, "n")
	if got, want := b.String(), "Delete all 5? [a/s/N] "; got != want {
		t.Errorf("Ask = %q, want %q", got, want)
	}
	b.Reset()
	r.Again(choices)
	if got, want := b.String(), "  answer a (all), s (select) or n (none)\n"; got != want {
		t.Errorf("Again = %q, want %q", got, want)
	}
	b.Reset()
	r.Choose(choices)
	if got, want := b.String(), "        a do all . s select . n none  > "; got != want {
		t.Errorf("Choose = %q, want %q", got, want)
	}
}

// ansi matches the colour codes the renderer writes.
var ansi = regexp.MustCompile("\x1b\\[[0-9]+m")

// TestRichDiffersOnlyInMarkers renders the same components plain and rich: once the colour codes are taken out and
// the rich symbols turned into their plain markers, the text is the same.
func TestRichDiffersOnlyInMarkers(t *testing.T) {
	draw := func(r *Renderer) {
		r.Heading("Ready to delete", Where{Roots: []string{"/home/u/Documents"}}, "2 files")
		r.Section("Past their retention", "2 files")
		r.Files(FileList{Columns: []Column{{Header: "until"}}, Rows: []FileRow{
			{Root: "/home/u/Documents", Rel: "a", Cells: []string{"2025-01-01"}},
			{Root: "/home/u/Documents", Rel: "b", Cells: []string{"2025-01-02"}}}, Max: 1, More: "files"})
		r.Dup(DupGroup{Name: "m", Each: "1 B", Keep: []Copy{{Root: "/home/u/Documents", Rel: "x"}},
			Delete: []Copy{{Root: "/home/u/Documents", Rel: "y", Symlinks: []string{"/home/u/l"}}}})
		r.Count(Done, "1 done")
		r.Count(Neutral, "1 left")
		r.Note(Attention, "look")
		r.Problems("Skipped", Attention, []Problem{{Root: "/home/u/Documents", Rel: "z", Reason: "changed"}})
		r.Item(1, 2, "past its retention", "1 B")
		r.ItemPath("delete", "/home/u/Documents", "z", "")
		r.Result(Done, "deleted")
	}
	p, pb := plainView()
	draw(p)
	rich, rb := termView(0, true)
	rich.o.Width = 0 // compare without the right-edge padding
	draw(rich)
	got := ansi.ReplaceAllString(rb.String(), "")
	// A marker starts a line's text: turn the rich symbol there into the plain marker.
	lines := strings.Split(got, "\n")
	for i, l := range lines {
		text := strings.TrimLeft(l, " ")
		for _, m := range marks {
			if rest, ok := strings.CutPrefix(text, m.symbol+" "); ok {
				lines[i] = l[:len(l)-len(text)] + m.plain + " " + rest
				break
			}
		}
	}
	got = strings.Join(lines, "\n")
	for from, to := range map[string]string{" · ": " . ", "…": "...", "↳": "->"} {
		got = strings.ReplaceAll(got, from, to)
	}
	if got != pb.String() {
		t.Errorf("rich without colour:\n%s\nplain:\n%s", got, pb.String())
	}
	if !strings.Contains(rb.String(), "\x1b[") {
		t.Error("the rich output has no colour codes")
	}
}

// TestSkipsNeverCollapse: more than ten skips with one reason, outside a CSV, are each listed by name: a run's
// output is its record.
func TestSkipsNeverCollapse(t *testing.T) {
	r, b := plainView()
	var ps []Problem
	for i := range 12 {
		ps = append(ps, Problem{Root: "/r", Rel: "f" + string(rune('a'+i)), Reason: "changed"})
	}
	r.Problems("Skipped", Attention, ps)
	for i := range 12 {
		if want := "f" + string(rune('a'+i)) + ": changed"; !strings.Contains(b.String(), want) {
			t.Errorf("skip %q missing:\n%s", want, b.String())
		}
	}
}

// TestTrailingSpaceKept: alignment is trimmed from a line's end; a space that ends a file name is shown, as \x20.
func TestTrailingSpaceKept(t *testing.T) {
	r, b := plainView()
	r.Heading("H", Where{Roots: []string{"/r"}})
	b.Reset()
	r.Files(FileList{Columns: []Column{{Header: "until"}, {Header: "size", Right: true}},
		Rows: []FileRow{{Root: "/r", Rel: "report ", Cells: []string{"2025-01-01", "1 B"}}}})
	r.ItemPath("delete", "/r", "report ", "")
	want := "    until        size\n    2025-01-01    1 B   report\\x20\n          delete   /r/report\\x20\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

func TestTilde(t *testing.T) {
	tests := []struct{ home, abs, want string }{
		{"/home/u", "/home/u/Documents", "~/Documents"},
		{"/home/u/", "/home/u/Documents", "~/Documents"},
		{"/home/u", "/home/u", "~"},
		{"/home/u", "/home/user2/x", "/home/user2/x"},
		{"/", "/etc", "/etc"},
		{"", "/home/u/x", "/home/u/x"},
	}
	for _, tt := range tests {
		r := New(Options{Out: &bytes.Buffer{}, Home: tt.home})
		if got := r.Tilde(tt.abs); got != tt.want {
			t.Errorf("Tilde(%q) with HOME %q = %q, want %q", tt.abs, tt.home, got, tt.want)
		}
	}
}

func TestColumns(t *testing.T) {
	tests := []struct {
		s    string
		want int
	}{
		{"report.pdf", 10},
		{"日本語.txt", 10},   // three wide characters and four narrow
		{"사진.jpg", 8},     // Hangul
		{"🏖 beach", 8},    // an emoji
		{"cafe\u0301", 4}, // a combining accent takes no column
		{"ＡＢ", 4},         // fullwidth forms
		{"a\u200db", 2},   // a zero-width joiner
		{"\\x1b[2K", 7},   // an escape as shown
	}
	for _, tt := range tests {
		if got := columns(tt.s); got != tt.want {
			t.Errorf("columns(%q) = %d, want %d", tt.s, got, tt.want)
		}
	}
}

// TestOneMore: a list cut one short says "1 more file", not "1 more files".
func TestOneMore(t *testing.T) {
	r, b := plainView()
	r.Files(FileList{Rows: []FileRow{{Root: "/r", Rel: "a"}, {Root: "/r", Rel: "b"}}, Max: 1, More: "files",
		MoreOne: "file"})
	if !strings.Contains(b.String(), "... 1 more file\n") {
		t.Errorf("got:\n%s", b.String())
	}
}

// TestTildeDir: a directory named ~ inside a root never reads as the home directory.
func TestTildeDir(t *testing.T) {
	r, b := plainView()
	r.Heading("H", Where{Roots: []string{"/data"}})
	b.Reset()
	r.Files(FileList{Rows: []FileRow{{Root: "/data", Rel: "~/secret.txt"}}})
	if got, want := b.String(), "    ./~/secret.txt\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := r.Path("/data", "~/secret.txt"); got != "./~/secret.txt" {
		t.Errorf("Path = %q", got)
	}
}

// TestNamesShowTrailingSpace: every place a file or directory name is shown makes a trailing space visible.
func TestNamesShowTrailingSpace(t *testing.T) {
	r, b := plainView()
	r.Heading("Ready", Where{Roots: []string{"/r "}, Under: "dir "}, "1 file")
	r.Dup(DupGroup{Name: "m", Each: "1 B", Keep: []Copy{{Root: "/r ", Rel: "a"}},
		Delete: []Copy{{Root: "/r ", Rel: "b", Symlinks: []string{"/r /link "}}}})
	r.ResultFile(Done, "deleted", "/r ", "b ", "")
	r.Files(FileList{Rows: []FileRow{{Root: "/other ", Rel: "c"}}, ByRoot: true})
	for _, want := range []string{"Ready   1 file . /r\\x20, under dir\\x20/\n", "the symlink /r /link\\x20 points",
		"[ok] deleted b\\x20\n", "  /other\\x20\n"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("lacks %q:\n%s", want, b.String())
		}
	}
}

// TestFailedMarker: in a list of skips, a failure keeps the refused marker.
func TestFailedMarker(t *testing.T) {
	r, b := plainView()
	r.Problems("Skipped", Attention, []Problem{{Root: "/r", Rel: "a", Reason: "changed"},
		{Root: "/r", Rel: "b", Reason: "permission denied", Failed: true}})
	for _, want := range []string{"[!] /r/a: changed", "[refused] /r/b: permission denied"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("lacks %q:\n%s", want, b.String())
		}
	}
}

// failWriter fails every write after the first n.
type failWriter struct{ n int }

func (w *failWriter) Write(p []byte) (int, error) {
	if w.n <= 0 {
		return 0, errors.New("no space left on device")
	}
	w.n--
	return len(p), nil
}

// TestWriteErrorKept: the first write error is kept, nothing more is written, and Err reports it.
func TestWriteErrorKept(t *testing.T) {
	w := &failWriter{n: 1}
	r := New(Options{Out: w})
	r.Next("one")
	if r.Err() != nil {
		t.Fatalf("error after a good write: %v", r.Err())
	}
	r.Next("two")
	r.Next("three")
	if r.Err() == nil || !strings.Contains(r.Err().Error(), "no space left on device") {
		t.Errorf("Err = %v, want the write error", r.Err())
	}
}

func TestFileRows(t *testing.T) {
	tests := []struct {
		name   string
		roots  []string // the heading's
		byRoot bool
		rows   []FileRow
		want   string
	}{
		{"a move: both paths are names", []string{"/r"}, false, []FileRow{{Root: "/r", From: "report ", Rel: "~/x"}},
			"    report\\x20 -> ./~/x\n"},
		{"a row with no path", []string{"/r"}, false, []FileRow{{Label: "file 12, no current path"}},
			"    (file 12, no current path)\n"},
		{"ordered by root: sub-headings", []string{"/a", "/b"}, true, []FileRow{{Root: "/a", Rel: "1"}, {Root: "/a", Rel: "2"},
			{Root: "/b", Rel: "3"}}, "  /a\n    1\n    2\n  /b\n    3\n"},
		{"not ordered by root: full paths", []string{"/a", "/b"}, false, []FileRow{{Root: "/a", Rel: "1"},
			{Root: "/b", Rel: "2"}, {Root: "/a", Rel: "3"}}, "    /a/1\n    /b/2\n    /a/3\n"},
		{"the heading's root, not ordered by root: relative, others full", []string{"/a"}, false, []FileRow{{Root: "/a", Rel: "1"},
			{Root: "/b", Rel: "2"}, {Root: "/a", Rel: "3"}}, "    1\n    /b/2\n    3\n"},
		{"the heading's root after another, ordered by root: both get a sub-heading", []string{"/a"}, true, []FileRow{{Root: "/b", Rel: "x"},
			{Root: "/b", Rel: "y"}, {Root: "/a", Rel: "z"}}, "  /b\n    x\n    y\n  /a\n    z\n"},
		{"a root of /", []string{"/"}, false, []FileRow{{Root: "/", Rel: "etc/x"}}, "    etc/x\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, b := plainView()
			r.Heading("H", Where{Roots: tt.roots})
			b.Reset()
			r.Files(FileList{Rows: tt.rows, ByRoot: tt.byRoot})
			if b.String() != tt.want {
				t.Errorf("got\n%q\nwant\n%q", b.String(), tt.want)
			}
		})
	}
}

// TestEntryAndDetailNames: a root entry and a detail path are names: a trailing space shows.
func TestEntryAndDetailNames(t *testing.T) {
	r, b := plainView()
	r.Entry(2, "/media/disk ", "1 file")
	r.DetailPath(4, "on the filesystem mounted at ", "/media ")
	want := "  /media/disk\\x20   1 file\n    on the filesystem mounted at /media\\x20\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// TestHardLinksUnderTheirCopy: a hard link is shown under its copy, as the same file, not as another copy.
func TestHardLinksUnderTheirCopy(t *testing.T) {
	r, b := plainView()
	r.Heading("H", Where{Roots: []string{"/r"}})
	b.Reset()
	r.Dup(DupGroup{Name: "a", Each: "1 B", Copies: []Copy{{Root: "/r", Rel: "a", Links: []string{"a2"}},
		{Root: "/r", Rel: "b"}}})
	want := "    a . 1 B each\n      a\n        hard link, the same file: a2\n      b\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

func TestSplitRootSlash(t *testing.T) {
	if root, rel := SplitRoot([]string{"/"}, "/etc/x"); root != "/" || rel != "etc/x" {
		t.Errorf("SplitRoot(/, /etc/x) = %q, %q", root, rel)
	}
}

// TestNotOrderedByRootNeverGroups: a list sorted by size whose roots happen to sit together still gets no
// sub-headings: one ranking, not one per root.
func TestNotOrderedByRootNeverGroups(t *testing.T) {
	r, b := plainView()
	r.Heading("H", Where{Roots: []string{"/a", "/b"}})
	b.Reset()
	r.Files(FileList{Rows: []FileRow{{Root: "/a", Rel: "1"}, {Root: "/a", Rel: "2"}, {Root: "/b", Rel: "3"}}})
	if want := "    /a/1\n    /a/2\n    /b/3\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// TestCrossRootMove: a file back from another root shows its earlier path in full, under that root, never under the
// root it is in now.
func TestCrossRootMove(t *testing.T) {
	r, b := plainView()
	r.Heading("H", Where{Roots: []string{"/a", "/b"}})
	b.Reset()
	r.Files(FileList{Rows: []FileRow{{Root: "/b", Rel: "y.txt", From: "x.txt", FromRoot: "/a"}}})
	if want := "    /a/x.txt -> /b/y.txt\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

// TestShortenRootSlash: with a root of /, filo's message text keeps its paths whole.
func TestShortenRootSlash(t *testing.T) {
	r, b := plainView()
	r.Heading("H", Where{Roots: []string{"/"}})
	b.Reset()
	r.Problems("Skipped", Attention, []Problem{{Root: "/", Rel: "data/b.pdf", Reason: "the copy kept, /data/a.pdf, " +
		"has a shorter retention"}})
	if !strings.Contains(b.String(), "the copy kept, /data/a.pdf, has a shorter retention") {
		t.Errorf("got %q", b.String())
	}
}

// TestShortenOnlyWherePathsStart: the heading's root is left out of a path that starts with it, never out of the
// middle of a longer path that holds it.
func TestShortenOnlyWherePathsStart(t *testing.T) {
	tests := []struct{ reason, want string }{
		{"/data/a.pdf changed", "a.pdf changed"},
		{"the copy kept, /data/a.pdf, has a shorter retention", "the copy kept, a.pdf, has a shorter retention"},
		{"the copy kept, /mnt/data/x.pdf, has a shorter retention", "the copy kept, /mnt/data/x.pdf, has a shorter retention"},
		{"kept (/data/a.pdf) and /data/b.pdf", "kept (a.pdf) and b.pdf"},
		{"/database/a.pdf", "/database/a.pdf"},
	}
	for _, tc := range tests {
		r, b := plainView()
		r.Heading("H", Where{Roots: []string{"/data"}})
		b.Reset()
		r.Problems("Skipped", Attention, []Problem{{Reason: tc.reason}})
		if !strings.Contains(b.String(), tc.want+"\n") {
			t.Errorf("reason %q: got %q, want it to end in %q", tc.reason, b.String(), tc.want)
		}
	}
}

// TestDupNameTrailingSpace: a duplicate group's name is a name: a trailing space shows.
func TestDupNameTrailingSpace(t *testing.T) {
	r, b := plainView()
	r.Dup(DupGroup{Name: "report ", Each: "1 B", Copies: []Copy{{Root: "/r", Rel: "report "}}})
	if !strings.HasPrefix(b.String(), "    report\\x20 . 1 B each\n") {
		t.Errorf("got %q", b.String())
	}
}

// TestTermsNarrow: a terminal too narrow for two columns gets each text under its name, within the width.
func TestTermsNarrow(t *testing.T) {
	var b strings.Builder
	r := New(Options{Out: &b, Terminal: true, Width: 35})
	r.Terms(2, 27, []Term{{Name: "--workers N", Text: "files hashed in parallel, 1-64 (default 4; use 1 on a spinning disk)"}})
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if lines[0] != "  --workers N" {
		t.Errorf("first line %q, want the name alone", lines[0])
	}
	for _, l := range lines {
		if len([]rune(l)) >= 35 {
			t.Errorf("line %q is not within 35 columns", l)
		}
	}
}

// TestGroups: rows under their group names, the columns lined up across groups, and every attention note in one
// column, the group's with its rows'.
func TestGroups(t *testing.T) {
	r, b := plainView()
	r.Groups([]TableGroup{
		{Name: "bank", Rows: []TableRow{{Name: "*DailyStatement*", Cells: []string{"daily", "7y", "212 files"}},
			{Name: "*", Cells: []string{"financial", "7y", "1 file"}}}},
		{Name: "scans/old ", Attention: "no such directory", Rows: []TableRow{{Name: "*",
			Cells: []string{"bills", "no value", "38 files"}, Attention: "needs a retention"}}},
	}, false, false, true)
	want := "" +
		"  bank\n" +
		"    *DailyStatement*   daily       7y         212 files\n" +
		"    *                  financial   7y            1 file\n" +
		"  scans/old\\x20                                           [!] no such directory\n" +
		"    *                  bills       no value    38 files   [!] needs a retention\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}

// TestHeadedTable: a table under a section, indented by four, below its dimmed headers, the columns lined up with
// them; a column with no header has none.
func TestHeadedTable(t *testing.T) {
	r, b := plainView()
	r.HeadedTable([]string{"directory", "pattern", "policy"}, []TableRow{
		{Name: "bank", Cells: []string{"*DailyStatement*", "daily-statements", "2 files", "1 needs a file_date"}},
		{Name: "personal/home/electric", Cells: []string{"*", "bills", "1 file"}},
	}, false, false, true)
	want := "" +
		"    directory                pattern            policy\n" +
		"    bank                     *DailyStatement*   daily-statements   2 files   1 needs a file_date\n" +
		"    personal/home/electric   *                  bills               1 file\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}
}
