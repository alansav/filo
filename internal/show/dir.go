package show

import (
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// Dir is one directory as filo show shows it.
type Dir struct {
	store.DirShown
	Reachable bool   // its root can be reached
	Why       string // why not
}

// LookDir checks the directory's root can be reached (mounted, still a directory).
func LookDir(d store.DirShown, root store.Root) Dir {
	if err := store.CheckRoot(root); err != nil {
		return Dir{DirShown: d, Why: err.Error()}
	}
	return Dir{DirShown: d, Reachable: true}
}

// parent is the directory a '/'-separated path inside a root is in ("" for the root).
func parent(rel string) string {
	if d := path.Dir(rel); d != "." {
		return d
	}
	return ""
}

// entryStatus is a file's status in a listing ("kept until 2029-01-31"), its place among copies ("has copies", or
// ""), and whether it needs a decision.
func entryStatus(e store.DirEntry) (string, string, bool) {
	var st string
	attention := false
	if !e.Present {
		st, attention = "missing since "+date(e.GoneUTC), true
	} else {
		switch e.Status {
		case store.StatusKept:
			st = "kept until " + e.KeepUntil
		case store.StatusForever:
			st = "forever"
		case store.StatusExpired:
			st, attention = "expired "+e.KeepUntil, true
		case store.StatusChanged:
			st, attention = "changed since its retention was set", true
		case store.StatusUndated:
			st, attention = "undated", true
		case store.StatusNeedsFileDate:
			st, attention = "needs a file_date", true
		case store.StatusPossibleMove:
			st, attention = "possible move", true
		case store.StatusUnreadable:
			st, attention = "unreadable", true
		default:
			st = string(e.Status)
		}
	}
	dup := ""
	switch e.Dup {
	case store.DupCopies:
		dup = "has copies"
	case store.DupRecord:
		dup = "a separate record"
	case store.DupOnPurpose:
		dup = "copies on purpose"
	}
	return st, dup, attention
}

// decisions says how many need a decision: "1 needs a decision", "3 need a decision", or "" for none.
func decisions(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "1 needs a decision"
	}
	return report.Count(int64(n)) + " need a decision"
}

// name is the directory's own name: its last part, or the root's.
func (d Dir) name() string {
	if d.Dir == "" {
		return filepath.Base(d.RootPath)
	}
	return path.Base(d.Dir)
}

// Render writes the directory: its title line, then its Directory and Rules parts and its contents. Without
// recursive, the contents are its subdirectories (each with its whole subtree's counts) and its own files; with it,
// a block per directory at it and below, each with its own files and the rules set on it.
func (d Dir) Render(v *view.Renderer, n, total int, recursive bool) {
	own := func(e store.DirEntry) bool { return parent(e.Path) == d.Dir }
	var files []store.DirEntry // the files the title and the Directory part count
	var size int64
	for _, e := range d.Files {
		if recursive || own(e) {
			files = append(files, e)
			size += e.Size
		}
	}
	dirs := d.subdirs(recursive)
	name, facts := d.name(), []string{}
	if recursive {
		name += " and below"
	}
	if len(dirs) > 0 {
		facts = append(facts, report.Plural(int64(len(dirs)), "directory", "directories"))
	}
	facts = append(facts, report.Plural(int64(len(files)), "file", "files"), report.Size(size))
	abs := store.AbsPath(d.RootPath, d.Dir)
	v.Subject(view.Subject{N: n, Total: total, Name: name, Status: "directory", Facts: facts, Dir: filepath.Dir(abs)})

	v.Part("Directory")
	root := view.Field{Label: "root", Value: d.RootPath, Path: true}
	if d.Reachable {
		root.Note = "reachable"
		if d.LastScanUTC != "" {
			root.Note = v.Join(root.Note, "last scan "+date(d.LastScanUTC))
		}
	} else {
		root.Attention = "not reachable: " + d.Why
	}
	v.Fields(0, []view.Field{{Label: "path", Value: abs, Path: true}, root,
		{Label: "files", Value: v.Join(d.counts(files, recursive)...)}})

	if len(d.Rules) == 0 {
		v.Part("Rules", "none")
	} else {
		v.Part("Rules", "in the order tried")
		v.HeadedTable(nil, ruleRows(v, d.RootPath, d.Rules))
	}

	if recursive {
		d.renderBlocks(v, dirs)
		return
	}
	if len(dirs) > 0 {
		v.Part("Directories", report.Count(int64(len(dirs))))
		rows := make([]view.ListRow, len(dirs))
		for i, sub := range dirs {
			var count, attention int
			var bytes int64
			for _, e := range d.Files {
				if store.IsAtOrUnder(e.Path, sub) {
					count++
					bytes += e.Size
					if _, _, a := entryStatus(e); a {
						attention++
					}
				}
			}
			rows[i] = view.ListRow{Name: path.Base(sub) + "/", Size: v.Join(report.Plural(int64(count), "file", "files"),
				report.Size(bytes)), Status: decisions(attention), Attention: attention > 0}
		}
		v.List(rows)
	}
	if len(files) == 0 {
		v.Part("Files", "none")
		return
	}
	v.Part("Files", report.Count(int64(len(files))))
	v.List(listRows(v, files))
}

// subdirs is the directories below d that hold files: its own subdirectories (recursive false), or every directory
// at any depth below it, those with no file of their own included where a deeper one has some (recursive true).
// Ordered as ls -R lists them: a parent before the directories inside it.
func (d Dir) subdirs(recursive bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range d.Files {
		p := parent(e.Path)
		for p != d.Dir && store.IsAtOrUnder(p, d.Dir) {
			if !recursive {
				if parent(p) == d.Dir && !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			} else if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			p = parent(p)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		return slices.Compare(strings.Split(a, "/"), strings.Split(b, "/"))
	})
	return out
}

// counts is the Directory part's files line, in parts: "12 present", "1 missing", "1 unreadable", "4 deleted by
// filo".
func (d Dir) counts(files []store.DirEntry, recursive bool) []string {
	var present, missing, unreadable int64
	for _, e := range files {
		switch {
		case !e.Present:
			missing++
		case e.Status == store.StatusUnreadable:
			unreadable++
		default:
			present++
		}
	}
	var deleted int64
	for _, p := range d.Deleted {
		if recursive || parent(p) == d.Dir {
			deleted++
		}
	}
	parts := []string{report.Count(present) + " present"}
	if missing > 0 {
		parts = append(parts, report.Count(missing)+" missing")
	}
	if unreadable > 0 {
		parts = append(parts, report.Count(unreadable)+" unreadable")
	}
	if deleted > 0 {
		parts = append(parts, report.Count(deleted)+" deleted by filo")
	}
	return parts
}

// ruleRows is the rules as table rows: the directory each is set on, its pattern, and its policy.
func ruleRows(v *view.Renderer, root string, rs []store.Rule) []view.TableRow {
	rows := make([]view.TableRow, len(rs))
	for i, r := range rs {
		rows[i] = view.TableRow{Name: v.Tilde(store.AbsPath(root, r.Dir)), Cells: []string{r.Pattern, policy(r)}}
	}
	return rows
}

// policy is a rule's policy with its value: "bills (7y)", "tax (no value yet)".
func policy(r store.Rule) string {
	if r.PolicyValue == "" {
		return r.Policy + " (no value yet)"
	}
	return r.Policy + " (" + r.PolicyValue + ")"
}

// listRows is files as listing rows: name, size, and status with its place among copies.
func listRows(v *view.Renderer, files []store.DirEntry) []view.ListRow {
	rows := make([]view.ListRow, len(files))
	for i, e := range files {
		st, dup, a := entryStatus(e)
		if dup != "" {
			st = v.Join(st, dup)
		}
		rows[i] = view.ListRow{Name: path.Base(e.Path), Size: report.Size(e.Size), Status: st, Attention: a}
	}
	return rows
}

// renderBlocks writes a block per directory, d's own first, then every directory below it (dirs) as ls -R does:
// each headed by its path from d's name, its file count and how many need a decision, then the rules set on it (d's
// own are in the Rules part) and its own files.
func (d Dir) renderBlocks(v *view.Renderer, dirs []string) {
	for _, dir := range append([]string{d.Dir}, dirs...) {
		var files []store.DirEntry
		attention := 0
		for _, e := range d.Files {
			if parent(e.Path) == dir {
				files = append(files, e)
				if _, _, a := entryStatus(e); a {
					attention++
				}
			}
		}
		head := d.name() + "/"
		if dir != d.Dir {
			head += strings.TrimPrefix(dir, d.Dir+"/") + "/"
			if d.Dir == "" {
				head = d.name() + "/" + dir + "/"
			}
		}
		count := report.Plural(int64(len(files)), "file", "files")
		if len(files) == 0 {
			count = "no files"
		}
		v.Block(head, decisions(attention), count)
		if dir != d.Dir {
			for _, r := range d.RulesBelow {
				if r.Dir == dir {
					v.Detail(4, "rule "+r.Pattern+" -> "+policy(r))
				}
			}
		}
		v.List(listRows(v, files))
	}
}
