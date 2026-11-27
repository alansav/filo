package tagindex

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyWriteTouchesTheDisk guards docs/design.md, Tag index pages, "Filo's own files": in package tagindex, only
// write.go creates, writes, renames or removes anything on the disk, so every change to an index's files goes
// through its checks that a file is still what Filo wrote; and it does so only through the directories it opened
// (*os.Root), never by path, so a directory swapped for a symlink is never followed. Any os call that changes the
// disk by path fails, in write.go too, and so does opening a directory (os.OpenRoot) anywhere else.
func TestOnlyWriteTouchesTheDisk(t *testing.T) {
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
				case sel.Sel.Name == "OpenRoot" && name != "write.go":
					t.Errorf("%s: os.OpenRoot outside write.go", fset.Position(sel.Pos()))
				}
			}
			return true
		})
	}
}
