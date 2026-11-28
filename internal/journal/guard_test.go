package journal

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestOnlyJournalWritesSyncedFiles guards CLAUDE.md's hard rule on a synced directory's .filo/ (docs/design.md,
// Sharing decisions between devices): in package journal, nothing changes the disk by path (no os call that
// creates, writes, renames or removes), only dir.go opens directories (os.OpenRoot), and so every change goes
// through the .filo/ it opened and checked; and outside this package no code names .filo at all, except the walk,
// which skips it.
func TestOnlyJournalWritesSyncedFiles(t *testing.T) {
	changing := map[string]bool{"Create": true, "CreateTemp": true, "WriteFile": true, "OpenFile": true,
		"Rename": true, "Remove": true, "RemoveAll": true, "Mkdir": true, "MkdirAll": true, "MkdirTemp": true,
		"Chmod": true, "Chown": true, "Chtimes": true, "Symlink": true, "Link": true, "Truncate": true}
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
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				pkg, ok := sel.X.(*ast.Ident)
				switch {
				case !ok || pkg.Name != "os":
				case changing[sel.Sel.Name]:
					t.Errorf("%s: os.%s changes the disk by path", fset.Position(sel.Pos()), sel.Sel.Name)
				case sel.Sel.Name == "OpenRoot" && name != "dir.go":
					t.Errorf("%s: os.OpenRoot outside dir.go", fset.Position(sel.Pos()))
				}
			}
			return true
		})
	}

	// The rest of the code: no string naming .filo, but for the walk's.
	err = filepath.WalkDir("../..", func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.IsDir() && (de.Name() == ".git" || de.Name() == "testdata") {
			return filepath.SkipDir
		}
		if de.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel("../..", p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/journal/") || rel == "internal/walk/walk.go" {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && (s == FiloDir || strings.HasPrefix(s, FiloDir+"/")) {
					t.Errorf("%s: names %s; only package journal reaches it", fset.Position(lit.Pos()), FiloDir)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
