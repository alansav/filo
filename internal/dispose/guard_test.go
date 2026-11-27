package dispose

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ownFiles are the only files outside package dispose that may delete or rename anything, each only Filo's own
// files (CLAUDE.md, Hard rules): the register's snapshots, an -o output file, and a tag index's pages.
var ownFiles = map[string]bool{
	"internal/store/snapshot.go": true,
	"internal/cli/output.go":     true,
	"internal/tagindex/write.go": true,
}

// TestOnlyDisposeChangesUserFiles guards the hard rule that internal/dispose is the only code that deletes or
// renames the user's files: in every Go file of the module but dispose's and ownFiles, any call that removes or
// renames (os.Remove, os.RemoveAll, os.Rename, os.Truncate, the syscall package's unlink and rename calls, or a
// Remove, RemoveAll or Rename method, such as os.Root's) fails. A call to another package's function of that name
// (tagindex.Remove) is that package's, and checked there.
func TestOnlyDisposeChangesUserFiles(t *testing.T) {
	changing := map[string]bool{"Remove": true, "RemoveAll": true, "Rename": true, "Truncate": true,
		"Unlink": true, "Unlinkat": true, "Renameat": true, "Renameat2": true, "Rmdir": true}
	module := filepath.Join("..", "..")
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir(module, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(module, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "internal/dispose" || strings.HasPrefix(d.Name(), ".") && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || ownFiles[rel] {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		checked++
		imported := map[string]string{} // name in the file -> import path
		for _, im := range f.Imports {
			ip, _ := strconv.Unquote(im.Path.Value)
			name := filepath.Base(ip)
			if im.Name != nil {
				name = im.Name.Name
			}
			imported[name] = ip
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !changing[sel.Sel.Name] {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok {
				if ip, isPkg := imported[pkg.Name]; isPkg && ip != "os" && ip != "syscall" && ip != "golang.org/x/sys/unix" {
					return true // another package's function: checked in that package
				}
			}
			t.Errorf("%s: %s removes or renames on the disk; only internal/dispose may change the user's files",
				fset.Position(sel.Pos()), sel.Sel.Name)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 50 {
		t.Fatalf("checked %d files: the module was not found", checked)
	}
}
