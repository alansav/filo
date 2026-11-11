package walk

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/fsid"
)

// build creates a tree under base. Keys are '/'-separated paths. A key ending in "/" is a directory, a value starting
// with "->" is a symlink to the rest of the value, anything else is a file with that content.
func build(t *testing.T, base string, spec map[string]string) {
	t.Helper()
	keys := make([]string, 0, len(spec))
	for k := range spec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := filepath.Join(base, filepath.FromSlash(strings.TrimSuffix(k, "/")))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		v := spec[k]
		switch {
		case strings.HasSuffix(k, "/"):
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
		case strings.HasPrefix(v, "->"):
			if err := os.Symlink(filepath.FromSlash(strings.TrimPrefix(v, "->")), p); err != nil {
				t.Fatal(err)
			}
		default:
			if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// fakeFS simulates other filesystems and bind mounts, which the container cannot create without privileges.
type fakeFS struct {
	devs    map[string]uint64 // absolute dir -> device for it and everything below it
	aliases map[string]string // absolute path -> absolute directory it is a bind mount of
}

func (f fakeFS) install(t *testing.T) {
	t.Helper()
	old := statID
	statID = func(p string) (fsid.ID, error) {
		if target, ok := f.aliases[p]; ok {
			p = target
		}
		id, err := fsid.Stat(p)
		if err != nil {
			return id, err
		}
		best := ""
		for prefix, dev := range f.devs {
			if (p == prefix || strings.HasPrefix(p, prefix+string(filepath.Separator))) && len(prefix) > len(best) {
				best, id.Dev = prefix, dev
			}
		}
		return id, nil
	}
	t.Cleanup(func() { statID = old })
}

// summary renders entries as path -> "file" | "symlink:TARGET" | "broken:TARGET".
func summary(entries []Entry) map[string]string {
	m := map[string]string{}
	for _, e := range entries {
		switch {
		case e.Kind == KindFile:
			m[e.Path] = "file"
		case e.LinkBroken:
			m[e.Path] = "broken:" + filepath.ToSlash(e.LinkTarget)
		default:
			m[e.Path] = "symlink:" + filepath.ToSlash(e.LinkTarget)
		}
	}
	return m
}

func canonicalTemp(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func TestWalk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("walk relies on device and inode lookup, not implemented on windows")
	}
	tests := []struct {
		name        string
		spec        map[string]string
		opt         func(base string) Options // Root is filled in if empty
		setup       func(t *testing.T, base string)
		want        map[string]string
		wantFailed  []string
		wantOtherFS []string
		problems    int // expected number of problems
		covers      map[string]bool
	}{
		{
			name: "plain files",
			spec: map[string]string{"a.txt": "a", "d/b.txt": "b", "d/e/c.txt": "c", "empty/": ""},
			want: map[string]string{"a.txt": "file", "d/b.txt": "file", "d/e/c.txt": "file"},
			covers: map[string]bool{
				"a.txt": true, "d/e/c.txt": true, "d/gone.txt": true,
				"x/y/z.txt": true, // x does not exist: covered, so old files there can go missing
			},
		},
		{
			name: "hidden skipped by default",
			spec: map[string]string{".h/x": "x", ".dot": "d", "v.txt": "v", "d/.inner": "i"},
			want: map[string]string{"v.txt": "file"},
			covers: map[string]bool{
				".h/x": false, ".dot": false, ".h/new/y": false, "d/.inner": false, "v.txt": true,
			},
		},
		{
			name: "hidden included",
			spec: map[string]string{".h/x": "x", ".dot": "d", "v.txt": "v"},
			opt:  func(string) Options { return Options{IncludeHidden: true} },
			want: map[string]string{".h/x": "file", ".dot": "file", "v.txt": "file"},
			covers: map[string]bool{
				".h/x": true, ".dot": true,
			},
		},
		{
			name: "noise names always skipped",
			spec: map[string]string{"node_modules/a": "a", "s/__pycache__/b": "b", ".filo/c": "c", "keep.txt": "k"},
			opt:  func(string) Options { return Options{IncludeHidden: true} },
			want: map[string]string{"keep.txt": "file"},
			covers: map[string]bool{
				"node_modules/a": false, "s/__pycache__/b": false, ".filo/c": false, "keep.txt": true,
			},
		},
		{
			name: "ignore names",
			spec: map[string]string{"build/x": "x", "sub/tmp.log": "t", "sub/keep": "k"},
			opt:  func(string) Options { return Options{Ignore: []string{"build", "tmp.log"}} },
			want: map[string]string{"sub/keep": "file"},
			covers: map[string]bool{
				"build/x": false, "sub/tmp.log": false, "sub/keep": true,
			},
		},
		{
			name: "bin and obj beside a project file",
			spec: map[string]string{
				"p/app.csproj": "<P/>", "p/bin/x": "x", "p/obj/y": "y", "p/src/z": "z",
				"q/lib.fsproj": "<P/>", "q/bin/a": "a",
				"r/bin/b": "b", // no project file: kept
			},
			want: map[string]string{
				"p/app.csproj": "file", "p/src/z": "file", "q/lib.fsproj": "file", "r/bin/b": "file",
			},
			covers: map[string]bool{"p/bin/x": false, "p/obj/y": false, "q/bin/a": false, "r/bin/b": true},
		},
		{
			name: "symlinks are recorded, never followed",
			spec: map[string]string{
				"f.txt": "f", "dir/in.txt": "i",
				"l-file": "->f.txt", "l-dir": "->dir", "l-none": "->nowhere",
				"loop1": "->loop2", "loop2": "->loop1",
			},
			want: map[string]string{
				"f.txt": "file", "dir/in.txt": "file",
				"l-file": "symlink:f.txt", "l-dir": "symlink:dir", "l-none": "broken:nowhere",
				"loop1": "broken:loop2", "loop2": "broken:loop1",
			},
			covers: map[string]bool{
				"l-dir":        true,
				"l-dir/in.txt": true, // l-dir is no longer a directory: anything once below it is covered
			},
		},
		{
			name: "scope limits the walk",
			spec: map[string]string{"a.txt": "a", "d/b.txt": "b", "d/e/c.txt": "c", "dd/x": "x"},
			opt:  func(string) Options { return Options{Scope: "d"} },
			want: map[string]string{"d/b.txt": "file", "d/e/c.txt": "file"},
			covers: map[string]bool{
				"a.txt": false, "dd/x": false, "d": false, "d/b.txt": true, "d/e/new": true,
			},
		},
		{
			name: "excluded paths",
			spec: map[string]string{"db/filo.db": "x", "db/filo.db-wal": "x", "db/filo.db.lock": "", "db/keep": "k"},
			opt: func(base string) Options {
				db := filepath.Join(base, "db", "filo.db")
				return Options{Exclude: []string{db, db + "-wal", db + "-shm", db + ".lock"}}
			},
			want:   map[string]string{"db/keep": "file"},
			covers: map[string]bool{"db/filo.db": false, "db/filo.db-wal": false, "db/keep": true},
		},
		{
			name: "unreadable directory",
			spec: map[string]string{"locked/x": "x", "locked/sub/y": "y", "other/z": "z"},
			setup: func(t *testing.T, base string) {
				if os.Geteuid() == 0 {
					t.Skip("permissions are not enforced for root")
				}
				locked := filepath.Join(base, "locked")
				if err := os.Chmod(locked, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(locked, 0o700) })
			},
			want:       map[string]string{"other/z": "file"},
			wantFailed: []string{"locked"},
			problems:   1,
			covers:     map[string]bool{"locked/x": false, "locked/sub/y": false, "other/z": true},
		},
		{
			name: "special files skipped",
			spec: map[string]string{"keep": "k"},
			setup: func(t *testing.T, base string) {
				if err := mkfifo(filepath.Join(base, "pipe")); err != nil {
					t.Skipf("mkfifo: %v", err)
				}
			},
			want:   map[string]string{"keep": "file"},
			covers: map[string]bool{"pipe": false},
		},
		{
			name: "other filesystem not entered",
			spec: map[string]string{"mnt/disk/x": "x", "home.txt": "h"},
			setup: func(t *testing.T, base string) {
				fakeFS{devs: map[string]uint64{filepath.Join(base, "mnt", "disk"): 999}}.install(t)
			},
			want:        map[string]string{"home.txt": "file"},
			wantOtherFS: []string{"mnt/disk"},
			covers:      map[string]bool{"mnt/disk/x": false, "mnt/other": true},
		},
		{
			name: "bind mount of an ancestor is not walked twice",
			spec: map[string]string{"d/back/x": "x", "d/f": "f"},
			setup: func(t *testing.T, base string) {
				fakeFS{aliases: map[string]string{filepath.Join(base, "d", "back"): base}}.install(t)
			},
			want:     map[string]string{"d/f": "file"},
			problems: 1,
			covers:   map[string]bool{"d/back/x": false, "d/f": true},
		},
		{
			name:   "ancestor replaced by a file",
			spec:   map[string]string{"gone": "now a file"},
			want:   map[string]string{"gone": "file"},
			covers: map[string]bool{"gone/x": true, "gone/a/b": true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := canonicalTemp(t)
			build(t, base, tc.spec)
			if tc.setup != nil {
				tc.setup(t, base)
			}
			opt := Options{}
			if tc.opt != nil {
				opt = tc.opt(base)
			}
			if opt.Root == "" {
				opt.Root = base
			}

			res, err := Walk(context.Background(), opt)
			if err != nil {
				t.Fatalf("Walk: %v", err)
			}
			got := summary(res.Entries)
			if len(got) != len(tc.want) {
				t.Errorf("entries = %v, want %v", got, tc.want)
			}
			for p, w := range tc.want {
				if got[p] != w {
					t.Errorf("entry %s = %q, want %q", p, got[p], w)
				}
			}
			if !equalStrings(res.FailedDirs, tc.wantFailed) {
				t.Errorf("FailedDirs = %v, want %v", res.FailedDirs, tc.wantFailed)
			}
			if !equalStrings(res.OtherFS, tc.wantOtherFS) {
				t.Errorf("OtherFS = %v, want %v", res.OtherFS, tc.wantOtherFS)
			}
			if len(res.Problems) != tc.problems {
				t.Errorf("Problems = %v, want %d", res.Problems, tc.problems)
			}
			for p, w := range tc.covers {
				if res.Covers(p) != w {
					t.Errorf("Covers(%q) = %v, want %v", p, !w, w)
				}
			}
		})
	}
}

func TestWalkEntryDetails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not implemented on windows")
	}
	base := canonicalTemp(t)
	build(t, base, map[string]string{"f.txt": "hello", "l": "->f.txt"})
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 6000, time.UTC)
	if err := os.Chtimes(filepath.Join(base, "f.txt"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	res, err := Walk(context.Background(), Options{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]Entry{}
	for _, e := range res.Entries {
		byPath[e.Path] = e
	}
	f := byPath["f.txt"]
	if f.Size != 5 || f.MtimeNS != mtime.UnixNano() || f.Kind != KindFile {
		t.Errorf("f.txt = %+v, want size 5, mtime %d", f, mtime.UnixNano())
	}
	l := byPath["l"]
	if l.Kind != KindSymlink || l.Size != int64(len("f.txt")) || l.LinkTarget != "f.txt" || l.LinkBroken {
		t.Errorf("l = %+v, want an unbroken symlink to f.txt with lstat size 5", l)
	}
}

func TestWalkErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not implemented on windows")
	}
	base := canonicalTemp(t)
	build(t, base, map[string]string{"d/f": "f", "file": "x", "mnt/disk/": ""})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name  string
		ctx   context.Context
		opt   Options
		setup func(t *testing.T)
		is    error // if set, errors.Is must hold
	}{
		{name: "relative root", opt: Options{Root: "rel"}},
		{name: "scope escapes root", opt: Options{Root: base, Scope: "../x"}},
		{name: "absolute scope", opt: Options{Root: base, Scope: "/etc"}},
		{name: "ignore with separator", opt: Options{Root: base, Ignore: []string{"a/b"}}},
		{name: "empty ignore", opt: Options{Root: base, Ignore: []string{""}}},
		{name: "scope missing", opt: Options{Root: base, Scope: "nope"}, is: ErrNotFound},
		{name: "scope is a file", opt: Options{Root: base, Scope: "file"}},
		{name: "scope on another filesystem", opt: Options{Root: base, Scope: "mnt/disk"}, setup: func(t *testing.T) {
			fakeFS{devs: map[string]uint64{filepath.Join(base, "mnt", "disk"): 999}}.install(t)
		}},
		{name: "cancelled", ctx: cancelled, opt: Options{Root: base}, is: context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup(t)
			}
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			res, err := Walk(ctx, tc.opt)
			if err == nil {
				t.Fatalf("Walk succeeded with %d entries, want an error", len(res.Entries))
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Fatalf("err = %v, want %v", err, tc.is)
			}
		})
	}
}

// TestTestTree walks the tree from scripts/testtree.sh before and after its mutation.
func TestTestTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not implemented on windows")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "testtree.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(canonicalTemp(t), "FiloTest")
	run := func(cmd string) {
		t.Helper()
		out, err := exec.Command(bash, script, cmd, dir).CombinedOutput()
		if err != nil {
			t.Fatalf("testtree.sh %s: %v\n%s", cmd, err, out)
		}
	}

	common := map[string]string{
		"photos/img1.bin": "file", "photos/img1-copy.bin": "file",
		"notes/ideas.md": "file", "notes/todo.md": "file",
		"projects/app/app.csproj": "file", "projects/app/src/main.cs": "file",
		"empty.txt": "file", "broken-link": "broken:nowhere",
		"docs/private.txt": "file", // listed even when unreadable: listing needs no read access to the file
	}
	// mutate makes locked/ unreadable. Root ignores permissions, so as root it is still listed.
	root := os.Geteuid() == 0
	mutated := map[string]string{
		"notes/contract-final.txt": "file", "photos/img2-renamed.bin": "file", "photos/img1-copy2.bin": "file",
		"notes/new-note.md": "file", "archive/report-2024.txt": "file",
		"link-to-report": "broken:docs/report-2024.txt",
	}
	var mutatedFailed []string
	if root {
		mutated["locked/kept.txt"] = "file"
	} else {
		mutatedFailed = []string{"locked"}
	}
	steps := []struct {
		cmd        string
		want       map[string]string
		wantFailed []string
	}{
		{"init", map[string]string{
			"docs/report-2024.txt": "file", "docs/contract.txt": "file", "docs/receipt-tyre.txt": "file",
			"photos/img2.bin": "file", "link-to-report": "symlink:docs/report-2024.txt", "locked/kept.txt": "file",
		}, nil},
		{"mutate", mutated, mutatedFailed},
	}
	t.Cleanup(func() {
		if _, err := os.Stat(dir); err == nil {
			run("unlock") // or the temp dir cannot be removed
		}
	})
	for _, st := range steps {
		run(st.cmd)
		res, err := Walk(context.Background(), Options{Root: dir})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{}
		for k, v := range common {
			want[k] = v
		}
		for k, v := range st.want {
			want[k] = v
		}
		got := summary(res.Entries)
		for p, w := range want {
			if got[p] != w {
				t.Errorf("after %s: %s = %q, want %q", st.cmd, p, got[p], w)
			}
		}
		for p := range got {
			if _, ok := want[p]; !ok {
				t.Errorf("after %s: unexpected entry %s", st.cmd, p)
			}
		}
		if len(res.Problems) != len(st.wantFailed) || !equalStrings(res.FailedDirs, st.wantFailed) {
			t.Errorf("after %s: problems %v, failed %v; want failed %v", st.cmd, res.Problems, res.FailedDirs,
				st.wantFailed)
		}
		if len(st.wantFailed) > 0 && res.Covers("locked/kept.txt") {
			t.Errorf("after %s: Covers(locked/kept.txt) = true under a directory that failed to list", st.cmd)
		}
		for _, p := range []string{"projects/app/bin/out.dll", ".hiddendir/secret.txt"} {
			if res.Covers(p) {
				t.Errorf("after %s: Covers(%s) = true, want false (skipped)", st.cmd, p)
			}
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSymlinkTargetUncheckable: a symlink whose target can't be checked (permission denied on the target's directory)
// is recorded, not marked broken, and reported as a problem. Never guessed either way.
func TestSymlinkTargetUncheckable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not implemented on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	base := canonicalTemp(t)
	build(t, base, map[string]string{"keep": "k"})
	// The locked directory is outside the root, so the walk itself never tries to list it.
	locked := filepath.Join(canonicalTemp(t), "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(locked, "x")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(base, "l")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })

	res, err := Walk(context.Background(), Options{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	got := summary(res.Entries)
	if got["l"] != "symlink:"+filepath.ToSlash(target) || got["keep"] != "file" || len(got) != 2 {
		t.Errorf("entries = %v, want keep and an unbroken symlink l", got)
	}
	if len(res.Problems) != 1 || res.Problems[0].Path != "l" ||
		!strings.Contains(res.Problems[0].Error(), "cannot check symlink target") {
		t.Errorf("problems = %v, want one for l", res.Problems)
	}
	if !res.Covers("l") {
		t.Error("the recorded symlink should be covered")
	}
}

func TestWalkRecordsInodes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not implemented on windows")
	}
	base := canonicalTemp(t)
	build(t, base, map[string]string{"f": "x", "l": "->f"})
	res, err := Walk(context.Background(), Options{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		want, err := fsid.Stat(filepath.Join(base, e.Path)) // lstat: the link's own inode
		if err != nil {
			t.Fatal(err)
		}
		if e.Ino == 0 || e.Ino != want.Ino {
			t.Errorf("%s: Ino = %d, want %d", e.Path, e.Ino, want.Ino)
		}
	}
	if len(res.Entries) != 2 {
		t.Errorf("entries = %v", res.Entries)
	}
}
