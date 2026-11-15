package cli

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestHelpGolden: filo help and filo help COMMAND, their whole plain output, wrapped at 80 columns as when piped
// (docs/design.md, Terminal output, Help and errors). One golden file per page.
func TestHelpGolden(t *testing.T) {
	ct := newCLITest(t)
	golden(t, "help.txt", ct.root, ct.filo(ExitOK, "", false, "help").stdout)
	for name := range commandHelps {
		golden(t, "help-"+name+".txt", ct.root, ct.filo(ExitOK, "", false, "help", name).stdout)
	}
	// No line is wider than 80 columns, except a single word too long to wrap.
	for name := range commandHelps {
		for _, line := range strings.Split(ct.filo(ExitOK, "", false, "help", name).stdout, "\n") {
			if len([]rune(line)) > 80 && strings.Contains(strings.TrimSpace(line), " ") {
				t.Errorf("help %s: line over 80 columns: %q", name, line)
			}
		}
	}
}

// TestErrorLine: an error is one line on stderr, escaped, a newline in it too: a name that holds one cannot forge a
// second "filo:" line. Filo's own joined errors are a line per part (closeStore, through joinedParts).
func TestErrorLine(t *testing.T) {
	var b bytes.Buffer
	e := &env{rawStderr: &b}
	e.errorLine("open /data/a\nfilo: 0 files deleted: a\x1b[2Jb")
	if want := "filo: open /data/a\\nfilo: 0 files deleted: a\\x1b[2Jb\n"; b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
	parts := joinedParts(errors.Join(errors.New("one"), errors.Join(errors.New("two"), errors.New("three\nx"))))
	var got []string
	for _, p := range parts {
		got = append(got, p.Error())
	}
	if want := []string{"one", "two", "three\nx"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("joinedParts = %q, want %q", got, want)
	}
}

// TestOutputGoesThroughTheRenderer guards docs/design.md, Help and errors: in internal/cli, stdout is written only
// by the Renderer and stderr only by errorLine and the progress line. Any other fmt.Fprint*, io.WriteString, use of
// os.Stdout or os.Stderr, or Write or WriteString on stdout or stderr fails, so an unescaped print cannot come back
// unnoticed. The writers allowed are named by their receiver and name, so another function called draw is not.
func TestOutputGoesThroughTheRenderer(t *testing.T) {
	allowed := map[string]bool{"env.errorLine": true, "progress.draw": true, "progress.clear": true,
		"firstError.Write": true} // firstError passes the Renderer's writes on
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || allowed[funcName(fn)] {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				bad := ""
				if pkg, ok := sel.X.(*ast.Ident); ok {
					switch {
					case pkg.Name == "fmt" && strings.HasPrefix(sel.Sel.Name, "Fprint"),
						pkg.Name == "io" && sel.Sel.Name == "WriteString",
						pkg.Name == "os" && (sel.Sel.Name == "Stdout" || sel.Sel.Name == "Stderr"):
						bad = pkg.Name + "." + sel.Sel.Name
					}
				}
				// Write or WriteString on the streams themselves: e.rawStderr, e.out, pr.e.rawStderr.
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Write" || sel.Sel.Name == "WriteString") &&
					(inner.Sel.Name == "rawStderr" || inner.Sel.Name == "out") {
					bad = inner.Sel.Name + "." + sel.Sel.Name
				}
				if bad != "" {
					t.Errorf("%s: %s writes with %s outside the Renderer and errorLine", fset.Position(sel.Pos()),
						funcName(fn), bad)
				}
				return true
			})
		}
	}
}

// funcName names a function as "Recv.Name" for a method (the receiver's type, without *), or "Name".
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}
