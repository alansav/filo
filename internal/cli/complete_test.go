package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alansav/filo/internal/complete"
)

// completeIn runs filo __complete for line (up to the cursor) and word (the part bash replaces, as typed), and
// returns its mode and candidates.
func completeIn(ct *cliTest, line, word string) (string, []string) {
	ct.t.Helper()
	r := ct.filo(ExitOK, "", false, "__complete", line, word)
	if r.stderr != "" {
		ct.t.Errorf("__complete wrote to stderr: %q", r.stderr)
	}
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	return lines[0], lines[1:]
}

// lastWord is what bash replaces at the end of line: from its last blank, colon or equals sign not escaped by a
// backslash (no quotes: enough for these tests).
func lastWord(line string) string {
	start := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case ' ', ':', '=':
			start = i + 1
		}
	}
	return line[start:]
}

// wantCompletion checks one completion's mode and candidates (in any order).
func wantCompletion(t *testing.T, ct *cliTest, line, mode string, want ...string) {
	t.Helper()
	gotMode, got := completeIn(ct, line, lastWord(line))
	if want == nil {
		want = []string{}
	}
	if got == nil || (len(got) == 1 && got[0] == "") {
		got = []string{}
	}
	slices.Sort(got)
	slices.Sort(want)
	if gotMode != mode || !slices.Equal(got, want) {
		t.Errorf("complete %q = %s %q, want %s %q", line, gotMode, got, mode, want)
	}
}

// completionTree is a root with files, tags, a policy, a rule, an index and a file deleted since, in a register.
func completionTree(t *testing.T) *cliTest {
	ct := newCLITest(t)
	ct.write("docs/bills 2026/gas.pdf", "gas")
	ct.write("docs/report.csv", "report")
	ct.write("docs/.hidden", "hidden")
	ct.write("docs/gone.txt", "gone")
	ct.write("notes/ideas.txt", "ideas")
	if err := os.MkdirAll(filepath.Join(ct.work, "elsewhere", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	ct.filo(ExitOK, "", false, "roots", "add", ct.root)
	ct.filo(ExitOK, "", false, "tag", ct.in("docs/bills 2026/gas.pdf"), "sender:British Gas", "type:bill", "urgent")
	ct.filo(ExitOK, "", false, "tag", ct.in("notes/ideas.txt"), "sender:Bob's Garage")
	ct.filo(ExitOK, "", false, "policies", "add", "bills", "7y")
	ct.filo(ExitOK, "", false, "rules", "add", ct.in("docs"), "*.pdf", "bills")
	ct.filo(ExitOK, "", false, "indexes", "add", ct.in("notes"), "--name", "ideas")
	if err := os.Remove(ct.in("docs/gone.txt")); err != nil {
		t.Fatal(err)
	}
	ct.filo(ExitOK, "", false, "scan")
	return ct
}

// TestCompleteCommandsAndFlags: the commands, subcommands and flags, from the help (docs/design.md, Bash completion).
func TestCompleteCommandsAndFlags(t *testing.T) {
	ct := completionTree(t)
	var all []string
	for name := range commandHelps {
		all = append(all, name+" ")
	}
	wantCompletion(t, ct, "filo ", "words", append(all, "help ")...)
	wantCompletion(t, ct, "filo ro", "words", "roots ")
	wantCompletion(t, ct, "filo --db x ro", "words", "roots ")
	wantCompletion(t, ct, "filo -", "words", "--db ")
	wantCompletion(t, ct, "filo help ta", "words", "tag ", "tags ")
	wantCompletion(t, ct, "filo roots ", "words", "add ", "set ")
	wantCompletion(t, ct, "filo indexes r", "words", "remove ", "refresh ")
	wantCompletion(t, ct, "filo roots add x --", "words", "--db ", "--include-hidden ", "--ignore ", "--no-scan ",
		"--workers ")
	wantCompletion(t, ct, "filo roots set x --", "words", "--db ", "--include-hidden ", "--skip-hidden ", "--ignore ")
	wantCompletion(t, ct, "filo set x --no", "words", "--no-file-date ", "--no-retention ")
	wantCompletion(t, ct, "filo get x --s", "words", "--status ", "--size ", "--sha256 ")
	wantCompletion(t, ct, "filo show x -", "words", "--db ", "-r ", "--recursive ", "--history ", "--verify ")
	wantCompletion(t, ct, "filo completion ", "words", "bash ")
	wantCompletion(t, ct, "filo nosuch ", "words")
	wantCompletion(t, ct, "filo set x --duplicates r", "words", "records ")
	wantCompletion(t, ct, "filo set x --duplicates=c", "words", "counted ")
	wantCompletion(t, ct, "filo scan --workers ", "words") // a number: nothing to offer
}

// TestCompletePaths: one level at a time, from the disk and the register (a file deleted since too), hidden entries
// only once a dot is typed, and directories ending in /. Each kind of path argument offers its own entries.
func TestCompletePaths(t *testing.T) {
	ct := completionTree(t)
	docs := ct.in("docs") + "/"
	wantCompletion(t, ct, "filo show "+docs, "files", docs+"bills 2026/", docs+"report.csv", docs+"gone.txt")
	wantCompletion(t, ct, "filo show "+docs+".", "files", docs+".hidden")
	wantCompletion(t, ct, "filo show "+docs+"bills\\ 2026/", "files", docs+"bills 2026/gas.pdf")
	wantCompletion(t, ct, "filo update "+docs, "files", docs+"bills 2026/", docs+"report.csv")
	// A directory argument: only directories, and outside the roots only those on the way to one.
	wantCompletion(t, ct, "filo scan "+docs, "files", docs+"bills 2026/")
	wantCompletion(t, ct, "filo scan "+ct.work+"/", "files", ct.work+"/home/")
	wantCompletion(t, ct, "filo roots add "+ct.work+"/", "files", ct.work+"/elsewhere/", ct.work+"/home/",
		ct.work+"/filo.db.snapshots/")
	// A subcommand and a directory, together: bash quotes both as file names.
	wantCompletion(t, ct, "filo rules ", "files", "add", "set", "remove", "move", "copy")
	wantCompletion(t, ct, "filo export d", "files", "duplicates")
	// -o FILE: any entry, a file not yet there being typed.
	wantCompletion(t, ct, "filo export -o "+docs+"r", "files", docs+"report.csv")
	wantCompletion(t, ct, "filo export --o="+docs+"r", "files", docs+"report.csv") // bash's word starts after =
	// ~/ is the home directory, kept as typed.
	ct.env["HOME"] = ct.root
	wantCompletion(t, ct, "filo show ~/no", "files", "~/notes/")
	wantCompletion(t, ct, "filo show ~", "files", "~/")
	wantCompletion(t, ct, "filo show ~bob/", "files")
	// A relative path, from the current directory.
	t.Chdir(ct.root)
	wantCompletion(t, ct, "filo show no", "files", "notes/")
}

// TestCompleteTags: keys then values, in any case, quoted by filo; tag and untag by their files-then-tags rule;
// untag only the files' own tags.
func TestCompleteTags(t *testing.T) {
	ct := completionTree(t)
	gas := strings.ReplaceAll(ct.in("docs/bills 2026/gas.pdf"), " ", `\ `)
	t.Chdir(t.TempDir()) // tag offers the files in the current directory too: none here
	wantCompletion(t, ct, "filo tag "+gas+" ", "words", "urgent ", "sender:", "type:")
	wantCompletion(t, ct, "filo tag "+gas+" se", "words", "sender:")
	wantCompletion(t, ct, "filo tag "+gas+" SENDER:b", "words", `British\ Gas `, `Bob\'s\ Garage `)
	wantCompletion(t, ct, "filo tag "+gas+" urgent ", "words", "urgent ", "sender:", "type:")
	wantCompletion(t, ct, "filo untag "+gas+" sender:", "words", " ", `British\ Gas `) // sender: is finished
	wantCompletion(t, ct, "filo untag "+ct.in("notes/ideas.txt")+" ", "words", "sender: ")
	wantCompletion(t, ct, "filo find --tag ty", "words", "type:")
	wantCompletion(t, ct, "filo find --no-tag=ur", "words", "urgent ") // bash's word starts after =
	wantCompletion(t, ct, "filo tags rename se", "words", "sender: ")
	wantCompletion(t, ct, "filo tags rename x se", "words", "sender:")
	// Inside a quote already open, the value goes on in it and is closed.
	l := `filo tag ` + gas + ` "sender:Bri`
	if mode, got := completeIn(ct, l, "sender:Bri"); mode != "words" || !slices.Equal(got, []string{`sender:British Gas" `}) {
		t.Errorf("in a quote: %s %q", mode, got)
	}
}

// TestCompleteRegistered: what acts on something registered offers what is registered: roots, indexes and their
// names, directories with rules and their patterns, policies.
func TestCompleteRegistered(t *testing.T) {
	ct := completionTree(t)
	wantCompletion(t, ct, "filo roots set "+ct.work, "files", ct.root)
	wantCompletion(t, ct, "filo indexes remove "+ct.work, "files", ct.in("notes"))
	wantCompletion(t, ct, "filo indexes refresh "+ct.in("notes")+" --name ", "words", "ideas ")
	wantCompletion(t, ct, "filo indexes add "+ct.in("notes")+" --name ", "words")
	wantCompletion(t, ct, "filo rules remove "+ct.work, "files", ct.in("docs"))
	wantCompletion(t, ct, "filo rules remove "+ct.in("docs")+" ", "words", `\*.pdf `)
	wantCompletion(t, ct, "filo rules add "+ct.in("docs")+" ", "words", "bills ")
	wantCompletion(t, ct, "filo policies show b", "words", "bills ")
	wantCompletion(t, ct, "filo policies add ", "words")
	wantCompletion(t, ct, "filo set x --retention b", "words", "bills ")
	ct.env["HOME"] = ct.work
	wantCompletion(t, ct, "filo roots set ~/", "files", "~/home")
}

// TestCompleteWithoutARegister: with no register, completion works from the disk, makes nothing, and says nothing.
func TestCompleteWithoutARegister(t *testing.T) {
	ct := newCLITest(t)
	ct.write("docs/a.txt", "a")
	ct.env["FILO_DB"] = filepath.Join(ct.work, "none", "filo.db")
	wantCompletion(t, ct, "filo show "+ct.in("docs")+"/", "files", ct.in("docs/a.txt"))
	t.Chdir(t.TempDir())
	wantCompletion(t, ct, "filo tag "+ct.in("docs/a.txt")+" ", "words") // no tags, no files here
	if _, err := os.Lstat(filepath.Join(ct.work, "none")); err == nil {
		t.Error("completion made the register's directory")
	}
	if _, err := os.Lstat(ct.db); err == nil {
		t.Error("completion made a register")
	}
	// A --db typed on the line is the register read.
	r := ct.filo(ExitOK, "", false, "__complete", "filo --db "+ct.work+"/x.db roots set ", "")
	if r.stdout != "files\n" {
		t.Errorf("with a --db that is not there: %q", r.stdout)
	}
	// Anything wrong is an empty answer, never an error.
	for _, args := range [][]string{{"__complete"}, {"__complete", "filo sh", "zz"}} {
		if r := ct.filo(ExitOK, "", false, args...); r.stderr != "" || strings.Count(r.stdout, "\n") > 1 {
			t.Errorf("filo %q = %q, %q", args, r.stdout, r.stderr)
		}
	}
}

// TestEveryPlaceholderHasASource: every argument of every usage line and every flag's value has a source, or is
// one of the new values completion does not guess (docs/design.md, Bash completion, "What each argument
// completes").
func TestEveryPlaceholderHasASource(t *testing.T) {
	for cmd, h := range commandHelps {
		for _, u := range complete.Usages(h.usage) {
			sub := ""
			if len(u.Words) > 1 {
				sub = u.Words[1]
			}
			for _, a := range u.Args {
				if argSource(cmd, sub, a.Name) == srcUnknown {
					t.Errorf("filo %s: no source for %s", strings.Join(u.Words, " "), a.Name)
				}
			}
			fs := u.Flags
			if u.AllFlags {
				fs = flagList(h)
			}
			for _, f := range fs {
				if f.Value != "" && flagSource(cmd, sub, f) == srcUnknown {
					t.Errorf("filo %s: no source for %s %s", strings.Join(u.Words, " "), f.Name, f.Value)
				}
			}
		}
	}
}

// TestSlots: which argument of a usage line an argument at a position can be.
func TestSlots(t *testing.T) {
	rulesAdd := complete.ParseUsage("filo rules add DIR [PATTERN] POLICY").Args
	tag := complete.ParseUsage("filo tag FILE... TAG...").Args
	for _, tc := range []struct {
		args []complete.Arg
		n    int
		want string
	}{
		{rulesAdd, 0, "DIR"}, {rulesAdd, 1, "PATTERN POLICY"}, {rulesAdd, 2, "POLICY"}, {rulesAdd, 3, ""},
		{tag, 0, "FILE"}, {tag, 1, "FILE TAG"}, {tag, 5, "FILE TAG"},
	} {
		var got []string
		for _, a := range slots(tc.args, tc.n) {
			got = append(got, a.Name)
		}
		slices.Sort(got)
		if strings.Join(got, " ") != tc.want {
			t.Errorf("slots(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
