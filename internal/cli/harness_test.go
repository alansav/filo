package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cliTest runs filo in-process against a throwaway register (work/filo.db, through FILO_DB) and a tree under
// work/home, with one device for every path (sameDevice). Tests take its methods as plain functions:
//
//	ct := newCLITest(t)
//	in, write, filo := ct.in, ct.write, ct.filo
type cliTest struct {
	t              *testing.T
	ctx            context.Context
	env            map[string]string
	work, root, db string
}

func newCLITest(t *testing.T) *cliTest {
	t.Helper()
	skipWindows(t)
	sameDevice(t, nil)
	work := canonicalTemp(t)
	db := filepath.Join(work, "filo.db")
	return &cliTest{t: t, ctx: context.Background(), env: map[string]string{"FILO_DB": db}, work: work,
		root: filepath.Join(work, "home"), db: db}
}

// in is the absolute path of rel ('/' separators) under the root.
func (c *cliTest) in(rel string) string { return filepath.Join(c.root, filepath.FromSlash(rel)) }

// write creates or replaces the file rel under the root, with its folders.
func (c *cliTest) write(rel, content string) {
	c.t.Helper()
	if err := os.MkdirAll(filepath.Dir(c.in(rel)), 0o755); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(c.in(rel), []byte(content), 0o644); err != nil {
		c.t.Fatal(err)
	}
}

// exists reports whether rel under the root exists (lstat: a symlink counts).
func (c *cliTest) exists(rel string) bool {
	_, err := os.Lstat(c.in(rel))
	return err == nil
}

// filo runs filo with stdin as standard input (a terminal if terminal is true) and fails the test unless it exits
// with wantCode.
func (c *cliTest) filo(wantCode int, stdin string, terminal bool, args ...string) result {
	c.t.Helper()
	var r result
	if terminal {
		r = runIn(c.t, c.ctx, c.env, terminalInput{strings.NewReader(stdin)}, args...)
	} else {
		r = runIn(c.t, c.ctx, c.env, strings.NewReader(stdin), args...)
	}
	if r.code != wantCode {
		c.t.Fatalf("filo %v: code %d, want %d\n%s%s", args, r.code, wantCode, r.stdout, r.stderr)
	}
	return r
}

// contains reports each of wants missing from out.
func (c *cliTest) contains(what, out string, wants ...string) {
	c.t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			c.t.Errorf("%s lacks %q:\n%s", what, w, out)
		}
	}
}

// lacks reports each of nots found in out.
func (c *cliTest) lacks(what, out string, nots ...string) {
	c.t.Helper()
	for _, n := range nots {
		if strings.Contains(out, n) {
			c.t.Errorf("%s has %q:\n%s", what, n, out)
		}
	}
}
