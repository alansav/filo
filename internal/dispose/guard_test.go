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

// ownFiles are the only files outside package dispose that may delete, rename or make anything, each only Filo's
// own files (CLAUDE.md, Hard rules), and each only with the calls named: the register's snapshots, its data
// directory, an -o output file, a tag index's pages, and a synced directory's .filo/ (the journal package).
var ownFiles = map[string]map[string]bool{
	"internal/store/snapshot.go": {"Remove": true, "Rename": true, "Mkdir": true},
	"internal/store/store.go":    {"MkdirAll": true},
	"internal/cli/output.go":     {"Remove": true, "Rename": true},
	"internal/tagindex/write.go": {"Remove": true, "Rename": true, "Mkdir": true},
	"internal/journal/dir.go":    {"Remove": true, "Rename": true, "Mkdir": true},
}

// TestOnlyDisposeChangesUserFiles guards the hard rule that internal/dispose is the only code that deletes, renames
// or moves the user's files, and makes the directories a move needs: in every Go file of the module but dispose's,
// any call that removes, renames or makes a directory (os.Remove, os.RemoveAll, os.Rename, os.Truncate, os.Mkdir,
// os.MkdirAll, the syscall package's unlink, rename and mkdir calls, or a method of those names, such as os.Root's)
// fails, but the calls ownFiles allows. A call to another package's function of that name (tagindex.Remove) is that
// package's, and checked there.
func TestOnlyDisposeChangesUserFiles(t *testing.T) {
	changing := map[string]bool{"Remove": true, "RemoveAll": true, "Rename": true, "Truncate": true,
		"Unlink": true, "Unlinkat": true, "Renameat": true, "Renameat2": true, "Rmdir": true, "Mkdir": true,
		"MkdirAll": true, "Mkdirat": true}
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
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		allowed := ownFiles[rel]
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
			if !ok || !changing[sel.Sel.Name] || allowed[sel.Sel.Name] {
				return true
			}
			pkg, isIdent := sel.X.(*ast.Ident)
			ip, isPkg := "", false
			if isIdent {
				ip, isPkg = imported[pkg.Name]
			}
			if isPkg && ip != "os" && ip != "syscall" && ip != "golang.org/x/sys/unix" {
				return true // another package's function: checked in that package
			}
			if sel.Sel.Name == "Truncate" && !isPkg {
				return true // a method of that name is time.Time's as often as a file's: only os.Truncate counts
			}
			t.Errorf("%s: %s changes the disk; only internal/dispose may change the user's files",
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
