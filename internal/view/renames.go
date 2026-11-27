package view

import "strings"

// RenameRow is one file an update renames in its own directory: where it is, its old and new names, and a note on a
// rename that may be an accident ("only spaces changed").
type RenameRow struct {
	Root string // the root, absolute
	Dir  string // the directory relative to it ('/' separators), "" for the root itself
	Old  string
	New  string
	Note string
}

// RenameList is the renames part of an update's summary (docs/design.md, Tags, "Renaming a file by its name cell").
type RenameList struct {
	Rows []RenameRow // in order of root and directory, so each directory is one group
	Max  int         // show at most this many, then "… N more files"; 0 = all
}

// Renames writes each directory (relative to the heading's root, or in full) and under it each rename, old and new
// name side by side: `Britsh bill.pdf  ->  British bill.pdf`. A name that starts or ends with a space is shown in
// double quotes, a quote in it as \", so the space can be seen; the note follows in brackets. A row that does not
// fit the terminal's width shows its new name on a line of its own.
func (r *Renderer) Renames(l RenameList) {
	rows := l.Rows
	more := 0
	if l.Max > 0 && len(rows) > l.Max {
		rows, more = rows[:l.Max], len(rows)-l.Max
	}
	oldWidth := 0
	for _, row := range rows {
		oldWidth = max(oldWidth, width(renameName(row.Old)))
	}
	for i, row := range rows {
		if i == 0 || row.Root != rows[i-1].Root || row.Dir != rows[i-1].Dir {
			r.write(spaces(4), name(r.renameDir(row.Root, row.Dir)))
		}
		old, nw := renameName(row.Old), renameName(row.New)
		tail := []span{plain("->"), spaces(2), nw}
		if row.Note != "" {
			tail = append(tail, spaces(3), styled("("+row.Note+")", dim))
		}
		line := []span{spaces(6), old, spaces(oldWidth - width(old) + 2)}
		if r.o.Width > 0 && width(append(line, tail...)...) >= r.o.Width {
			r.write(spaces(6), old)
			r.write(append([]span{spaces(8)}, tail...)...)
			continue
		}
		r.write(append(line, tail...)...)
	}
	if more > 0 {
		r.More(more, "file", "files")
	}
}

// renameDir is a directory of the renames list: relative to the heading's root, or in full, ending in /.
func (r *Renderer) renameDir(root, dir string) string {
	if dir == "" {
		if root != "" && root == r.root {
			return "./"
		}
		return r.Tilde(root) + "/"
	}
	return r.Path(root, dir) + "/"
}

// renameName is a name in the renames list: as any name (escaped), or, when it starts or ends with a space, in double
// quotes with a quote in it shown as \", so "notes.txt " and "notes.txt" never read the same.
func renameName(s string) span {
	if strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") {
		return escaped(`"`+strings.ReplaceAll(Escape(s), `"`, `\"`)+`"`, "")
	}
	return name(s)
}
