package view

// MoveRow is one file an update moves to another directory of its root: where it goes, from where, its new name if
// that changes too, a note on a move that may be an accident, and the symlinks that would break.
type MoveRow struct {
	Root     string   // the root, absolute
	Dir      string   // the directory it goes to, relative to the root ('/' separators), "" for the root itself
	New      bool     // the directory does not exist yet: the update makes it
	From     string   // the file's path now, relative to the root
	Name     string   // its new name, "" if it keeps its name
	Note     string   // such as "extension changed"
	Symlinks []string // registered symlinks that point at it (absolute)
}

// MoveList is the moves part of an update's summary (docs/design.md, Tags, "Moving a file by its directory cell").
type MoveList struct {
	Rows []MoveRow // in order of root and destination, so each destination is one group
	Max  int       // show at most this many, then "… N more files"; 0 = all
}

// Moves writes each destination directory (relative to the heading's root, or in full; "(new)" when the update
// makes it) and under it each file moved there, by its path now, then "as NAME" for a new name, and its note;
// each symlink that would break follows on a line of its own. A row too wide for the terminal puts "as NAME" on a
// line of its own.
func (r *Renderer) Moves(l MoveList) {
	rows := l.Rows
	more := 0
	if l.Max > 0 && len(rows) > l.Max {
		rows, more = rows[:l.Max], len(rows)-l.Max
	}
	for i, row := range rows {
		if i == 0 || row.Root != rows[i-1].Root || row.Dir != rows[i-1].Dir {
			head := []span{spaces(4), name(r.renameDir(row.Root, row.Dir))}
			if row.New {
				head = append(head, spaces(3), styled("(new)", dim))
			}
			r.write(head...)
		}
		from := name(r.Path(row.Root, row.From))
		var tail []span
		if row.Name != "" {
			tail = append(tail, plain("as"), spaces(2), renameName(row.Name))
		}
		if row.Note != "" {
			if len(tail) > 0 {
				tail = append(tail, spaces(3))
			}
			tail = append(tail, styled("("+row.Note+")", dim))
		}
		line := []span{spaces(6), from}
		switch {
		case len(tail) == 0:
			r.write(line...)
		case r.o.Width > 0 && width(append(append(line, spaces(3)), tail...)...) >= r.o.Width:
			r.write(line...)
			r.write(append([]span{spaces(8)}, tail...)...)
		default:
			r.write(append(append(line, spaces(3)), tail...)...)
		}
		for _, s := range row.Symlinks {
			r.write(spaces(8), plain(r.arrow()+" the symlink "), name(r.Tilde(s)), plain(" points here and would break"))
		}
	}
	if more > 0 {
		r.More(more, "file", "files")
	}
}
