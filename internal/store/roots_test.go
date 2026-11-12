package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/fsid"
)

// fakeFS simulates what the container cannot create without privileges. Every path not under an override gets
// device baseDev, so tests do not depend on how the machine running them is mounted.
type fakeFS struct {
	devs    map[string]uint64 // directory -> device for it and everything below it (deepest prefix wins)
	aliases map[string]string // path -> the directory it is a bind mount of
}

// install swaps statID for the fake until the test ends.
func (f *fakeFS) install(t *testing.T) {
	t.Helper()
	old := statID
	statID = f.stat
	t.Cleanup(func() { statID = old })
}

func (f *fakeFS) stat(path string) (fsid.ID, error) {
	if target, ok := f.aliases[path]; ok {
		path = target
	}
	id, err := fsid.Stat(path)
	if err != nil {
		return id, err
	}
	best := ""
	id.Dev = baseDev
	for prefix, dev := range f.devs {
		if (path == prefix || isWithin(path, prefix)) && len(prefix) > len(best) {
			best, id.Dev = prefix, dev
		}
	}
	return id, nil
}

const baseDev = 1

// tree makes a canonical temp dir containing the given subdirectories.
func tree(t *testing.T, dirs ...string) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(base, filepath.FromSlash(d)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

func skipNonUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("device and inode lookup not implemented on windows")
	}
}

func TestAddRoot(t *testing.T) {
	skipNonUnix(t)
	type step struct {
		path    string   // relative to the tree base
		wantErr error    // nil = must succeed; errAny = any error
		mount   string   // expected mount path on success, relative to base; "" = none
		absorbs []string // roots it absorbs (relative to base), on success
	}

	tests := []struct {
		name    string
		dirs    []string
		file    string // a regular file to create, relative to base
		fake    fakeFS // paths relative to base; made absolute before install
		symlink [2]string
		steps   []step
	}{
		{
			name:  "two separate roots",
			dirs:  []string{"home", "disk"},
			steps: []step{{path: "home"}, {path: "disk"}},
		},
		{
			name:  "same path twice",
			dirs:  []string{"home"},
			steps: []step{{path: "home"}, {path: "home", wantErr: ErrRootExists}},
		},
		{
			name:    "alias through a symlink",
			dirs:    []string{"home"},
			symlink: [2]string{"home", "link"},
			steps:   []step{{path: "home"}, {path: "link", wantErr: ErrRootExists}},
		},
		{
			name:  "alias through a bind mount",
			dirs:  []string{"home", "mnt/home"},
			fake:  fakeFS{aliases: map[string]string{"mnt/home": "home"}},
			steps: []step{{path: "home"}, {path: "mnt/home", wantErr: ErrRootExists}},
		},
		{
			name:  "inside a root through a bind mount, same filesystem",
			dirs:  []string{"home/docs", "mnt/home/docs"},
			fake:  fakeFS{aliases: map[string]string{"mnt/home": "home"}},
			steps: []step{{path: "home"}, {path: "mnt/home/docs", wantErr: ErrRootOverlap}},
		},
		{
			name:  "nested on the same filesystem, inner second",
			dirs:  []string{"home/docs"},
			steps: []step{{path: "home"}, {path: "home/docs", wantErr: ErrRootOverlap}},
		},
		{
			name:  "nested on the same filesystem, outer second: absorbed",
			dirs:  []string{"home/docs"},
			steps: []step{{path: "home/docs"}, {path: "home", absorbs: []string{"home/docs"}}},
		},
		{
			name: "two roots absorbed at once",
			dirs: []string{"home/Documents", "home/Pictures", "other"},
			steps: []step{{path: "home/Documents"}, {path: "home/Pictures"}, {path: "other"},
				{path: "home", absorbs: []string{"home/Documents", "home/Pictures"}}},
		},
		{
			name:  "absorbed below an alias of the new root (a bind mount)",
			dirs:  []string{"home/docs", "mnt/home/docs"},
			fake:  fakeFS{aliases: map[string]string{"mnt/home": "home"}},
			steps: []step{{path: "mnt/home/docs"}, {path: "home", absorbs: []string{"mnt/home/docs"}}},
		},
		{
			name: "only the same filesystem is absorbed; a mounted disk inside stays a root",
			dirs: []string{"home/docs", "home/mnt/disk"},
			fake: fakeFS{devs: map[string]uint64{"home/mnt/disk": 999}},
			steps: []step{{path: "home/docs"}, {path: "home/mnt/disk", mount: "home/mnt/disk"},
				{path: "home", absorbs: []string{"home/docs"}}},
		},
		{
			name:  "nested across filesystems, inner second",
			dirs:  []string{"home/mnt/disk"},
			fake:  fakeFS{devs: map[string]uint64{"home/mnt/disk": 999}},
			steps: []step{{path: "home"}, {path: "home/mnt/disk", mount: "home/mnt/disk"}},
		},
		{
			name:  "nested across filesystems, outer second",
			dirs:  []string{"home/mnt/disk"},
			fake:  fakeFS{devs: map[string]uint64{"home/mnt/disk": 999}},
			steps: []step{{path: "home/mnt/disk", mount: "home/mnt/disk"}, {path: "home"}},
		},
		{
			name: "two roots nested inside one other filesystem",
			dirs: []string{"home/mnt/disk/photos"},
			fake: fakeFS{devs: map[string]uint64{"home/mnt/disk": 999}},
			steps: []step{
				{path: "home/mnt/disk", mount: "home/mnt/disk"},
				{path: "home/mnt/disk/photos", wantErr: ErrRootOverlap},
			},
		},
		{
			name:  "root inside a mount records the mount point",
			dirs:  []string{"home", "mnt/disk/photos"},
			fake:  fakeFS{devs: map[string]uint64{"mnt/disk": 999}},
			steps: []step{{path: "home"}, {path: "mnt/disk/photos", mount: "mnt/disk"}},
		},
		{
			name:  "a file is not a root",
			dirs:  []string{"home"},
			file:  "home/f.txt",
			steps: []step{{path: "home/f.txt", wantErr: errAny}},
		},
		{
			name:  "missing directory",
			steps: []step{{path: "nope", wantErr: errAny}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := tree(t, tc.dirs...)
			abs := func(rel string) string { return filepath.Join(base, filepath.FromSlash(rel)) }
			if tc.file != "" {
				if err := os.WriteFile(abs(tc.file), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.symlink[0] != "" {
				if err := os.Symlink(abs(tc.symlink[0]), abs(tc.symlink[1])); err != nil {
					t.Fatal(err)
				}
			}
			f := fakeFS{devs: map[string]uint64{}, aliases: map[string]string{}}
			for k, v := range tc.fake.devs {
				f.devs[abs(k)] = v
			}
			for k, v := range tc.fake.aliases {
				f.aliases[abs(k)] = abs(v)
			}
			f.install(t)

			s := mustOpen(t, Options{DBPath: tempDB(t)})
			defer mustClose(t, s)
			added := 0
			for i, st := range tc.steps {
				r, absorbed, err := s.AddRootAbsorb(context.Background(), abs(st.path), Settings{})
				switch {
				case st.wantErr == nil:
					if err != nil {
						t.Fatalf("step %d AddRoot(%s): %v", i, st.path, err)
					}
					added++
					wantMount := ""
					if st.mount != "" {
						wantMount = abs(st.mount)
					}
					if r.Path != abs(st.path) || r.MountPath != wantMount || r.ID == 0 {
						t.Errorf("step %d: got %+v, want path %s, mount path %q", i, r, abs(st.path), wantMount)
					}
					var got, want []string
					for _, a := range absorbed {
						got = append(got, a.Path)
					}
					for _, a := range st.absorbs {
						want = append(want, abs(a))
					}
					if !slices.Equal(got, want) {
						t.Errorf("step %d absorbed %v, want %v", i, got, want)
					}
					added -= len(absorbed)
				case st.wantErr == errAny:
					if err == nil {
						t.Fatalf("step %d AddRoot(%s) succeeded, want an error", i, st.path)
					}
				default:
					if !errors.Is(err, st.wantErr) {
						t.Fatalf("step %d AddRoot(%s) err = %v, want %v", i, st.path, err, st.wantErr)
					}
				}
			}
			roots, err := s.Roots(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(roots) != added {
				t.Errorf("%d roots stored, want %d (refused roots must not be stored)", len(roots), added)
			}
		})
	}
}

func TestCheckRootAndUnmounted(t *testing.T) {
	skipNonUnix(t)
	base := tree(t, "home/mnt/disk/photos", "plain", "mnt/usb/photos")
	disk := filepath.Join(base, "home/mnt/disk")
	home := filepath.Join(base, "home")
	usb := filepath.Join(base, "mnt/usb")
	mounted := &fakeFS{devs: map[string]uint64{disk: 999, usb: 555}}
	unmounted := &fakeFS{}

	mounted.install(t)
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	diskRoot, err := s.AddRoot(context.Background(), disk)
	if err != nil {
		t.Fatal(err)
	}
	plainRoot, err := s.AddRoot(context.Background(), filepath.Join(base, "plain"))
	if err != nil {
		t.Fatal(err)
	}
	photosRoot, err := s.AddRoot(context.Background(), filepath.Join(usb, "photos"))
	if err != nil {
		t.Fatal(err)
	}
	if diskRoot.MountPath != disk || plainRoot.MountPath != "" || photosRoot.MountPath != usb {
		t.Fatalf("mount paths = %q, %q, %q", diskRoot.MountPath, plainRoot.MountPath, photosRoot.MountPath)
	}
	// The disk is mounted again, but the root's directory now holds a different filesystem.
	remounted := &fakeFS{devs: map[string]uint64{usb: 555, filepath.Join(usb, "photos"): 556}}

	tests := []struct {
		name    string
		fs      *fakeFS
		run     func() error
		wantErr error // nil = success; errAny = any error
	}{
		{"mounted disk passes", mounted, func() error { return CheckRoot(diskRoot) }, nil},
		{"plain root passes", unmounted, func() error { return CheckRoot(plainRoot) }, nil},
		{"unmounted disk refused", unmounted, func() error { return CheckRoot(diskRoot) }, ErrNotMounted},
		{"root inside a mount passes while mounted", mounted, func() error { return CheckRoot(photosRoot) }, nil},
		{"root inside an unmounted mount refused", unmounted, func() error { return CheckRoot(photosRoot) }, ErrNotMounted},
		{"root no longer on its mount refused", remounted, func() error { return CheckRoot(photosRoot) }, ErrNotMounted},
		{"resolve under unmounted disk refused", unmounted, func() error {
			_, _, err := s.Resolve(context.Background(), filepath.Join(disk, "photos"))
			return err
		}, ErrNotMounted},
		{"adding an outer root while the inner disk is unmounted is refused", unmounted, func() error {
			_, err := s.AddRoot(context.Background(), home)
			return err
		}, ErrRootOverlap},
		{"missing root refused", unmounted, func() error {
			return CheckRoot(Root{Path: filepath.Join(base, "gone")})
		}, errAny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fs.install(t)
			err := tc.run()
			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("err = %v, want nil", err)
			case tc.wantErr == errAny:
				if err == nil {
					t.Fatal("succeeded, want an error")
				}
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	skipNonUnix(t)
	base := tree(t, "home/docs/old", "home/mnt/disk/photos", "home/mnt/usb/x", "mnt/home/docs", "elsewhere")
	if err := os.WriteFile(filepath.Join(base, "home/docs/f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	abs := func(rel string) string { return filepath.Join(base, filepath.FromSlash(rel)) }
	f := &fakeFS{
		devs: map[string]uint64{abs("home/mnt/disk"): 999, abs("home/mnt/usb"): 777},
		// mnt/home is a bind mount of home, so mnt/home/docs is home/docs.
		aliases: map[string]string{abs("mnt/home"): abs("home"), abs("mnt/home/docs"): abs("home/docs")},
	}
	f.install(t)

	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	for _, r := range []string{"home", "home/mnt/disk"} {
		if _, err := s.AddRoot(context.Background(), abs(r)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(abs("home/docs"), abs("docs-link")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		target    string
		wantRoot  string
		wantScope string
		wantErr   error // errAny for "any error"
	}{
		{name: "the root itself", target: "home", wantRoot: "home", wantScope: ""},
		{name: "a subdirectory", target: "home/docs/old", wantRoot: "home", wantScope: "docs/old"},
		{name: "through a symlink", target: "docs-link", wantRoot: "home", wantScope: "docs"},
		{name: "through a bind mount", target: "mnt/home/docs", wantRoot: "home", wantScope: "docs"},
		{name: "deepest nested root wins", target: "home/mnt/disk/photos", wantRoot: "home/mnt/disk", wantScope: "photos"},
		{name: "other filesystem under a root", target: "home/mnt/usb/x", wantErr: errAny},
		{name: "not under any root", target: "elsewhere", wantErr: ErrNoRoot},
		{name: "a file", target: "home/docs/f.txt", wantErr: errAny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, scope, err := s.Resolve(context.Background(), abs(tc.target))
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("Resolve succeeded (%s, %q), want an error", r.Path, scope)
				}
				if tc.wantErr != errAny && !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Path != abs(tc.wantRoot) || scope != tc.wantScope {
				t.Errorf("got (%s, %q), want (%s, %q)", r.Path, scope, abs(tc.wantRoot), tc.wantScope)
			}
		})
	}
}

var errAny = errors.New("any error")

func TestAbsorbKeepsHistoryAndReviews(t *testing.T) {
	skipNonUnix(t)
	(&fakeFS{}).install(t)
	ctx := context.Background()
	db := tempDB(t)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	base := tree(t, "home/Documents/tax", "home/Pictures", "home/notes")
	abs := func(rel string) string { return filepath.Join(base, filepath.FromSlash(rel)) }
	docs, err := s.AddRoot(ctx, abs("home/Documents"))
	if err != nil {
		t.Fatal(err)
	}
	pics, err := s.AddRoot(ctx, abs("home/Pictures"))
	if err != nil {
		t.Fatal(err)
	}
	apply := func(root Root, scope string, ch Changes) int64 {
		t.Helper()
		id, err := s.ApplyScan(ctx, ScanRecord{RootID: root.ID, Scope: scope, Started: time.Now()}, ch)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	file := func(p, h string) NewFile { return NewFile{Path: p, Kind: "file", Size: 1, MtimeNS: 1, Hash: h} }
	apply(docs, "", Changes{New: []NewFile{file("tax/2024.pdf", "h1"), file("old.txt", "h2")}})
	apply(pics, "", Changes{New: []NewFile{file("beach.jpg", "h3")}})
	kd := map[string]Known{}
	ks, _ := s.Known(ctx, docs.ID)
	for _, k := range ks {
		kd[k.Path] = k
	}
	// A move inside Documents, a scoped scan of Documents, and a file returned to Pictures from Documents.
	apply(docs, "", Changes{Close: []int64{kd["old.txt"].PathID}, Open: []OpenPath{{FileID: kd["old.txt"].FileID,
		Path: "tax/old.txt"}}, Events: []Event{{Kind: "moved", FileID: kd["old.txt"].FileID, Path: "tax/old.txt",
		OtherPath: "old.txt"}}})
	apply(docs, "tax", Changes{})
	apply(pics, "", Changes{Events: []Event{{Kind: "moved", FileID: 99, Path: "beach.jpg", OtherPath: "x/beach.jpg",
		Detail: "returned (missing since scan 1) from root " + abs("home/Documents")}}})

	// A keep_until date on a Documents file.
	q, err := s.Files(ctx, "2026-11-11", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	exportID, err := s.RecordExport(ctx, "", now, q, func(int64) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.UpdateLifespan(ctx, []LifespanRow{{Line: 2, ExportID: exportID, FileID: kd["tax/2024.pdf"].FileID,
		Retention: "2032-01-01"}}, now, "2026-11-11", "r.csv", false, nil); err != nil || res.Set != 1 {
		t.Fatalf("update: %+v, %v", res, err)
	}
	if _, err := s.SetSettings(ctx, abs("home/Pictures"), Settings{Ignore: []string{"raw"}}); err != nil {
		t.Fatal(err)
	}

	home, absorbed, err := s.AddRootAbsorb(ctx, abs("home"), Settings{IncludeHidden: true, Ignore: []string{"tmp", "tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Absorbed{{Path: abs("home/Documents"), Prefix: "Documents"},
		{Path: abs("home/Pictures"), Prefix: "Pictures", Settings: Settings{Ignore: []string{"raw"}}}}
	if len(absorbed) != len(want) {
		t.Fatalf("absorbed = %+v, want %+v", absorbed, want)
	}
	for i := range want {
		if absorbed[i].Path != want[i].Path || absorbed[i].Prefix != want[i].Prefix ||
			!absorbed[i].Settings.Equal(want[i].Settings) {
			t.Errorf("absorbed[%d] = %+v, want %+v", i, absorbed[i], want[i])
		}
	}
	if !home.Settings.Equal(Settings{IncludeHidden: true, Ignore: []string{"tmp"}}) {
		t.Errorf("new root settings = %+v, want hidden included, tmp ignored once", home.Settings)
	}
	roots, err := s.Roots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].ID != home.ID || !roots[0].Settings.Equal(home.Settings) {
		t.Fatalf("roots = %+v, want only the new one, with its settings", roots)
	}

	// Same files, same IDs, paths under their folders.
	ks, err = s.Known(ctx, home.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, k := range ks {
		got[k.Path] = k.FileID
	}
	if len(got) != 3 || got["Documents/tax/2024.pdf"] != kd["tax/2024.pdf"].FileID ||
		got["Documents/tax/old.txt"] != kd["old.txt"].FileID || got["Pictures/beach.jpg"] == 0 {
		t.Errorf("known after absorb = %v", got)
	}
	// History intervals moved too.
	hist, err := s.History(ctx, kd["old.txt"].FileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 || hist[0].Path != "Documents/old.txt" || hist[1].Path != "Documents/tax/old.txt" ||
		hist[0].RootID != home.ID {
		t.Errorf("history = %+v", hist)
	}

	// Scans became scoped scans of the new root.
	if v := rawQueryString(t, db, `SELECT group_concat(scope, '|') FROM (SELECT scope FROM scans ORDER BY id)`); v !=
		"Documents|Pictures|Documents|Documents/tax|Pictures" {
		t.Errorf("scopes = %q", v)
	}
	if v := rawQueryString(t, db, `SELECT count(*) FROM scans WHERE root_id != `+strconv.FormatInt(home.ID, 10)); v != "0" {
		t.Errorf("%s scans left on old roots", v)
	}
	// Event paths: prefixed, except a returned file's path in the root it came from.
	if v := rawQueryString(t, db, `SELECT path || ' <- ' || other_path FROM events WHERE detail IS NULL`); v !=
		"Documents/tax/old.txt <- Documents/old.txt" {
		t.Errorf("moved event = %q", v)
	}
	if v := rawQueryString(t, db, `SELECT path || ' <- ' || other_path FROM events WHERE detail LIKE 'returned%'`); v !=
		"Pictures/beach.jpg <- x/beach.jpg" {
		t.Errorf("returned event = %q", v)
	}

	// The keep_until date survived the absorb.
	q, err = s.Files(ctx, "2026-11-11", Filter{RootID: home.ID, Scope: "Documents"})
	if err != nil {
		t.Fatal(err)
	}
	got2 := map[string]string{}
	for _, it := range q {
		got2[it.Path] = it.KeepUntil + " " + string(it.Status)
	}
	if len(got2) != 2 || got2["Documents/tax/2024.pdf"] != "2032-01-01 kept" || got2["Documents/tax/old.txt"] != " undated" {
		t.Errorf("files under Documents = %v", got2)
	}
}

func TestAbsorbRefusesAnUnreachableRoot(t *testing.T) {
	skipNonUnix(t)
	(&fakeFS{}).install(t)
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	base := tree(t, "home/Documents", "home/gone")
	if _, err := s.AddRoot(ctx, filepath.Join(base, "home", "Documents")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoot(ctx, filepath.Join(base, "home", "gone")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(base, "home", "gone")); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.AddRootAbsorb(ctx, filepath.Join(base, "home"), Settings{})
	if !errors.Is(err, ErrRootOverlap) || !strings.Contains(err.Error(), "cannot be checked now") {
		t.Fatalf("err = %v, want a refusal naming the unreachable root", err)
	}
	roots, err := s.Roots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 {
		t.Errorf("roots = %+v: a refused add must change nothing", roots)
	}
}
