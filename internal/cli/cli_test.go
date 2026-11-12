package cli

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/alansav/filo/internal/fsid"
	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/store"
)

type result struct {
	code           int
	stdout, stderr string
}

// run executes the CLI in-process with only the given environment, and an empty standard input that is not a
// terminal.
func run(t *testing.T, ctx context.Context, env map[string]string, args ...string) result {
	t.Helper()
	return runIn(t, ctx, env, strings.NewReader(""), args...)
}

// runIn is run with stdin as standard input.
func runIn(t *testing.T, ctx context.Context, env map[string]string, stdin io.Reader, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(ctx, args, stdin, &out, &errb, func(k string) string { return env[k] })
	return result{code, out.String(), errb.String()}
}

func canonicalTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// sameDevice makes every path report one device, so tests do not depend on how this machine is mounted.
func sameDevice(t *testing.T, override map[string]uint64) {
	t.Helper()
	t.Cleanup(fsid.SetForTest(func(p string, real func(string) (fsid.ID, error)) (fsid.ID, error) {
		id, err := real(p)
		if err != nil {
			return id, err
		}
		id.Dev = 1
		for prefix, dev := range override {
			if p == prefix || strings.HasPrefix(p, prefix+string(filepath.Separator)) {
				id.Dev = dev
			}
		}
		return id, nil
	}))
}

func skipWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("device and inode lookup not implemented on windows")
	}
}

func TestParseInterspersed(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		pos     []string
		hidden  bool
		ignores []string
		db      string
		wantErr bool
	}{
		{name: "flags after the path", args: []string{"/p", "--include-hidden"}, pos: []string{"/p"}, hidden: true},
		{name: "flags before the path", args: []string{"--include-hidden", "/p"}, pos: []string{"/p"}, hidden: true},
		{name: "repeated ignore around the path", args: []string{"--ignore", "a", "/p", "--ignore=b"},
			pos: []string{"/p"}, ignores: []string{"a", "b"}},
		{name: "db after the path", args: []string{"/p", "--db", "/x.db"}, pos: []string{"/p"}, db: "/x.db"},
		{name: "double dash ends flags", args: []string{"--", "--include-hidden"}, pos: []string{"--include-hidden"}},
		{name: "double dash after a path", args: []string{"/p", "--", "-x"}, pos: []string{"/p", "-x"}},
		{name: "unknown flag", args: []string{"/p", "--nope"}, wantErr: true},
		{name: "missing flag value", args: []string{"/p", "--db"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			fs.SetOutput(new(bytes.Buffer))
			hidden := fs.Bool("include-hidden", false, "")
			db := fs.String("db", "", "")
			var ign stringList
			fs.Var(&ign, "ignore", "")
			pos, err := parseInterspersed(fs, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(pos, tc.pos) || *hidden != tc.hidden || *db != tc.db ||
				!reflect.DeepEqual([]string(ign), tc.ignores) {
				t.Errorf("pos %q hidden %v db %q ignores %q", pos, *hidden, *db, ign)
			}
		})
	}
}

func TestUsageAndHelp(t *testing.T) {
	db := filepath.Join(t.TempDir(), "filo.db")
	tests := []struct {
		name     string
		args     []string
		code     int
		inStdout string
		inStderr string
	}{
		{"no arguments prints usage", nil, ExitOK, "Usage:", ""},
		{"help", []string{"help"}, ExitOK, "filo roots add DIR", ""},
		{"-h", []string{"-h"}, ExitOK, "Usage:", ""},
		{"help roots", []string{"help", "roots"}, ExitOK, "--include-hidden", ""},
		{"help export", []string{"help", "export"}, ExitOK, "keep_until", ""},
		{"scan no longer takes root settings", []string{"--db", db, "scan", "--include-hidden"}, ExitUsage, "",
			"include-hidden"},
		{"review is gone", []string{"--db", db, "review"}, ExitUsage, "", "export lifespan and update"},
		{"unknown roots command", []string{"--db", db, "roots", "bogus"}, ExitUsage, "", `unknown roots command "bogus"`},
		{"roots set without a dir", []string{"--db", db, "roots", "set"}, ExitUsage, "", "exactly one DIR"},
		{"roots set both ways", []string{"--db", db, "roots", "set", "x", "--include-hidden", "--skip-hidden"},
			ExitUsage, "", "not both"},
		{"roots add with a bad ignore", []string{"--db", db, "roots", "add", "x", "--ignore", "a/b"}, ExitUsage, "",
			"plain file or directory name"},
		{"scan -h", []string{"scan", "-h"}, ExitOK, "--allow-empty", ""},
		{"unknown command", []string{"bogus"}, ExitUsage, "", `unknown command "bogus"`},
		{"unknown flag", []string{"scan", "--bogus"}, ExitUsage, "", "bogus"},
		{"workers out of range", []string{"--db", db, "scan", "--workers", "0"}, ExitUsage, "", "--workers"},
		{"two dirs", []string{"--db", db, "scan", "a", "b"}, ExitUsage, "", "at most one DIR"},
		{"roots add without a dir", []string{"--db", db, "roots", "add"}, ExitUsage, "", "exactly one DIR"},
		{"report with an argument", []string{"--db", db, "report", "x"}, ExitUsage, "", "no arguments"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, context.Background(), nil, tc.args...)
			if r.code != tc.code || !strings.Contains(r.stdout, tc.inStdout) || !strings.Contains(r.stderr, tc.inStderr) {
				t.Errorf("code %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
			}
		})
	}
}

func TestDBSelection(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	root := canonicalTemp(t)
	envDB := filepath.Join(t.TempDir(), "env.db")
	flagDB := filepath.Join(t.TempDir(), "flag.db")
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string // the file that must exist afterwards
	}{
		{"FILO_DB", map[string]string{"FILO_DB": envDB}, []string{"roots", "add", root, "--no-scan"}, envDB},
		{"--db before the command beats FILO_DB", map[string]string{"FILO_DB": envDB},
			[]string{"--db", flagDB, "roots"}, flagDB},
		{"--db after the command", nil, []string{"roots", "--db", flagDB + "2"}, flagDB + "2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if r := run(t, context.Background(), tc.env, tc.args...); r.code != ExitOK {
				t.Fatalf("code %d: %s", r.code, r.stderr)
			}
			if _, err := os.Stat(tc.want); err != nil {
				t.Errorf("database not at %s: %v", tc.want, err)
			}
		})
	}
}

func TestEndToEnd(t *testing.T) {
	skipWindows(t)
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	sameDevice(t, nil)
	env := map[string]string{"FILO_DB": filepath.Join(t.TempDir(), "filo.db")}
	tree := filepath.Join(canonicalTemp(t), "FiloTest")
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "testtree.sh"))
	if err != nil {
		t.Fatal(err)
	}
	sh := func(cmd string) {
		t.Helper()
		if out, err := exec.Command(bash, script, cmd, tree).CombinedOutput(); err != nil {
			t.Fatalf("testtree.sh %s: %v\n%s", cmd, err, out)
		}
	}
	ctx := context.Background()
	must := func(args ...string) string {
		t.Helper()
		r := run(t, ctx, env, args...)
		if r.code != ExitOK {
			t.Fatalf("filo %v: code %d\n%s%s", args, r.code, r.stdout, r.stderr)
		}
		return r.stdout
	}

	sh("init")
	// roots add scans the new root straight away.
	if out := must("roots", "add", tree); !strings.Contains(out, "Added root "+tree+" (hidden files skipped)") ||
		!strings.Contains(out, "15 files seen") || !strings.Contains(out, "new 15 ") {
		t.Errorf("roots add: %s", out)
	}
	if out := must("roots"); !strings.Contains(out, "last scan") || !strings.Contains(out, "settings: hidden files skipped") {
		t.Errorf("roots: %s", out)
	}
	sh("mutate")
	t.Cleanup(func() { sh("unlock") }) // or the temp dir cannot be removed
	// mutate makes a directory and a file unreadable; root ignores permissions, so as root there are no errors.
	asRoot := os.Geteuid() == 0
	wantSecond := []string{
		"moved     docs/contract.txt -> notes/contract-final.txt",
		"possible  docs/report-2024.txt -> archive/report-2024.txt ?",
	}
	if asRoot {
		wantSecond = append(wantSecond, "new 3   changed 2   moved 2   missing 2   possible moves 1   errors 0")
	} else {
		wantSecond = append(wantSecond, "new 3   changed 2   moved 2   missing 2   possible moves 1   errors 2",
			"error     docs/private.txt (hash: ", "error     locked (")
	}
	out := must("scan") // every root
	for _, want := range wantSecond {
		if !strings.Contains(out, want) {
			t.Errorf("second scan output lacks %q:\n%s", want, out)
		}
	}

	out = must("report", "--top", "3")
	photos := filepath.Join(tree, "photos")
	for _, want := range []string{
		"Duplicates: 1 group, 3 files, 195 KiB reclaimable",
		"3 copies of 97.7 KiB, 195 KiB reclaimable",
		filepath.Join(photos, "img1-copy.bin"), filepath.Join(photos, "img1-copy2.bin"),
		"Broken symlinks: 2",
		filepath.Join(tree, "broken-link") + " -> nowhere",
		"117 KiB  " + filepath.Join(photos, "img2-renamed.bin"), // largest first
		"last scan 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	wantUnreadable := "Unreadable files: 1\n  since scan 2  " + filepath.Join(tree, "docs", "private.txt") + "\n"
	if asRoot {
		wantUnreadable = "Unreadable files: none\n"
	}
	if !strings.Contains(out, wantUnreadable) {
		t.Errorf("report lacks %q:\n%s", wantUnreadable, out)
	}
}

func TestScanErrors(t *testing.T) {
	skipWindows(t)
	ctx := context.Background()
	tests := []struct {
		name     string
		setup    func(t *testing.T, env map[string]string) []string // returns the args
		code     int
		inStdout string
		inStderr string
	}{
		{"no roots yet", func(t *testing.T, env map[string]string) []string {
			return []string{"scan"}
		}, ExitError, "", "no roots yet; add one with: filo roots add DIR"},
		{"path outside every root", func(t *testing.T, env map[string]string) []string {
			return []string{"scan", canonicalTemp(t)}
		}, ExitError, "", "filo roots add"},
		{"database locked by another process", func(t *testing.T, env map[string]string) []string {
			s, err := store.Open(ctx, store.Options{DBPath: env["FILO_DB"]})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return []string{"roots"}
		}, ExitError, "", "locked by another filo process"},
		{"one unmounted root: the other is still scanned", func(t *testing.T, env map[string]string) []string {
			base := canonicalTemp(t)
			disk, home := filepath.Join(base, "disk"), filepath.Join(base, "home")
			for _, d := range []string{disk, home} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(home, "f"), []byte("f"), 0o600); err != nil {
				t.Fatal(err)
			}
			sameDevice(t, map[string]uint64{disk: 2})
			for _, r := range []string{disk, home} {
				if r := run(t, ctx, env, "roots", "add", r, "--no-scan"); r.code != ExitOK {
					t.Fatal(r.stderr)
				}
			}
			sameDevice(t, nil) // now unmounted
			return []string{"scan"}
		}, ExitError, "new 1 ", "root looks unmounted"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sameDevice(t, nil)
			env := map[string]string{"FILO_DB": filepath.Join(t.TempDir(), "filo.db")}
			args := tc.setup(t, env)
			r := run(t, ctx, env, args...)
			if r.code != tc.code || !strings.Contains(r.stdout, tc.inStdout) || !strings.Contains(r.stderr, tc.inStderr) {
				t.Errorf("code %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
			}
			if strings.Count(strings.TrimSpace(r.stderr), "\n") > 0 && tc.name != "one unmounted root: the other is still scanned" {
				t.Errorf("error should be one line: %q", r.stderr)
			}
		})
	}
}

func TestCancelledScanWritesNothing(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	root := canonicalTemp(t)
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("f"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FILO_DB": filepath.Join(t.TempDir(), "filo.db")}
	if r := run(t, context.Background(), env, "roots", "add", root, "--no-scan"); r.code != ExitOK {
		t.Fatal(r.stderr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := run(t, ctx, env, "scan", root); r.code != ExitError {
		t.Errorf("code %d, want %d", r.code, ExitError)
	}
	s, err := store.Open(context.Background(), store.Options{DBPath: env["FILO_DB"]})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.RowCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rows["scans"] != 0 || rows["files"] != 0 {
		t.Errorf("rows after a cancelled scan: %v", rows)
	}
}

// fakeTerminal is a buffer that claims to be a terminal.
type fakeTerminal struct{ bytes.Buffer }

func (*fakeTerminal) IsTerminal() bool { return true }

func TestProgress(t *testing.T) {
	t.Run("line per phase", func(t *testing.T) {
		p := &reconcile.Progress{}
		p.Phase.Store(reconcile.PhaseHashing)
		p.HashFiles.Store(52310)
		p.HashBytes.Store(118 << 30)
		p.Hash.Files.Store(1204)
		p.Hash.Bytes.Store(3 << 30)
		if got := progressLine(p); !strings.Contains(got, "hashing 1,204 / 52,310 files, 3.0 GiB / 118 GiB") {
			t.Errorf("hashing line = %q", got)
		}
		p.Phase.Store(reconcile.PhaseWalking)
		p.Walked.Store(12345)
		if got := progressLine(p); !strings.Contains(got, "walking, 12,345 entries") {
			t.Errorf("walking line = %q", got)
		}
		p.Phase.Store(reconcile.PhaseIdle)
		if got := progressLine(p); got != "" {
			t.Errorf("idle line = %q, want none", got)
		}
	})
	t.Run("terminal gets a cleared line, a plain writer gets nothing", func(t *testing.T) {
		term := &fakeTerminal{}
		e := &env{stderr: term}
		opt := reconcile.Options{}
		stop := e.startProgress(&opt)
		if opt.Progress == nil {
			t.Fatal("no progress attached for a terminal")
		}
		stop()
		stop() // safe twice
		if !strings.Contains(term.String(), "\r\x1b[K") {
			t.Errorf("terminal output %q lacks a line clear", term.String())
		}

		var plain bytes.Buffer
		e = &env{stderr: &plain}
		opt = reconcile.Options{}
		e.startProgress(&opt)()
		if opt.Progress != nil || plain.Len() != 0 {
			t.Errorf("plain writer: progress %v, output %q", opt.Progress, plain.String())
		}
	})
}

// TestBinary builds the real command and runs it, to check main.go's wiring and exit codes.
func TestBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	skipWindows(t)
	bin := filepath.Join(t.TempDir(), "filo")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/alansav/filo/cmd/filo").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	root := canonicalTemp(t)
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("f"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "filo.db")
	tests := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"help"}, 0, "Usage:"},
		{[]string{"roots", "add", root}, 0, "new 1 "},
		{[]string{"scan", root}, 0, "new 0 "},
		{[]string{"bogus"}, 2, "unknown command"},
	}
	for _, tc := range tests {
		cmd := exec.Command(bin, tc.args...)
		// A minimal environment: no HOME, so nothing can fall back to a real data directory.
		cmd.Env = []string{"FILO_DB=" + db, "PATH=" + os.Getenv("PATH")}
		out, _ := cmd.CombinedOutput()
		if code := cmd.ProcessState.ExitCode(); code != tc.code || !strings.Contains(string(out), tc.want) {
			t.Errorf("filo %v: code %d, output %s", tc.args, code, out)
		}
	}
}

func TestRootsAddAbsorbs(t *testing.T) {
	skipWindows(t)
	sameDevice(t, nil)
	dir := canonicalTemp(t)
	home := filepath.Join(dir, "home")
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Documents/tax/2024.pdf", "tax")
	write("Pictures/📷 beach.jpg", "sand")
	write(".config/app.conf", "x")
	write("notes.txt", "n")
	env := map[string]string{"FILO_DB": filepath.Join(dir, "filo.db")}
	ctx := context.Background()
	must := func(args ...string) string {
		t.Helper()
		r := run(t, ctx, env, args...)
		if r.code != ExitOK {
			t.Fatalf("filo %v: code %d\n%s%s", args, r.code, r.stdout, r.stderr)
		}
		return r.stdout
	}
	in := func(rel string) string { return filepath.Join(home, filepath.FromSlash(rel)) }

	must("roots", "add", in("Documents"))
	must("roots", "add", in("Pictures"), "--ignore", "raw")
	must("roots", "add", in(".config"))

	// Adding home absorbs the three and scans home with its own settings: only notes.txt is new; Documents and
	// Pictures are known; .config is skipped, not missing.
	out := must("roots", "add", home)
	for _, want := range []string{
		"Added root " + home + " (hidden files skipped)",
		"It now includes the roots " + in(".config") + ", " + in("Documents") + " and " + in("Pictures") +
			", with their history and keep_until dates.",
		"Note: " + in(".config") + " is skipped by the settings of " + home + ", so its files are no longer scanned",
		"Note: " + in("Pictures") + " had other settings (hidden files skipped; ignoring raw); the settings of " +
			home + " apply to it now.",
		"new 1   changed 0   moved 0   missing 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("roots add lacks %q:\n%s", want, out)
		}
	}
	var listed []string
	for _, l := range strings.Split(must("roots"), "\n") {
		if l != "" && !strings.HasPrefix(l, " ") {
			listed = append(listed, l)
		}
	}
	if !reflect.DeepEqual(listed, []string{home}) {
		t.Errorf("roots after absorbing = %q, want only %s", listed, home)
	}

	// Including hidden files brings .config back: its file is known, not new.
	out = must("roots", "set", home, "--include-hidden")
	if !strings.Contains(out, "settings were hidden files skipped; now hidden files included") {
		t.Errorf("roots set:\n%s", out)
	}
	if out := must("scan"); !strings.Contains(out, "new 0   changed 0   moved 0   missing 0") {
		t.Errorf("scan after including hidden files:\n%s", out)
	}
	if out := must("roots", "set", home, "--include-hidden"); !strings.Contains(out, "settings unchanged") {
		t.Errorf("roots set again:\n%s", out)
	}
	if r := run(t, ctx, env, "roots", "set", in("Documents")); r.code != ExitError ||
		!strings.Contains(r.stderr, "is not a root") {
		t.Errorf("roots set on a folder inside a root: code %d\n%s", r.code, r.stderr)
	}

	// The reverse: a folder inside a root is not a new root.
	r := run(t, ctx, env, "roots", "add", in("Documents"))
	if r.code != ExitError || !strings.Contains(r.stderr, "is already inside root "+home+
		"; scan just that folder with: filo scan "+in("Documents")) {
		t.Errorf("adding a folder inside a root: code %d\n%s", r.code, r.stderr)
	}
}
