package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

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
		path    string // relative to the tree base
		wantErr error  // nil = must succeed; errAny = any error
		mount   string // expected mount path on success, relative to base; "" = none
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
			name:  "nested on the same filesystem, outer second",
			dirs:  []string{"home/docs"},
			steps: []step{{path: "home/docs"}, {path: "home", wantErr: ErrRootOverlap}},
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
				r, err := s.AddRoot(context.Background(), abs(st.path))
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
