package view

// Column is one fixed column of a file list, before the path: a date, a size, a retention.
type Column struct {
	Header string // shown above the column when any column has one
	Right  bool   // right-aligned (sizes)
}

// FileRow is one file in a list: where it is, its cells for the columns, and an optional note after the path.
type FileRow struct {
	Root, Rel string // the root, absolute, and the path relative to it ('/' separators)
	Cells     []string
	Note      string
}

// FileList is a list of files under a section.
type FileList struct {
	Columns []Column
	Rows    []FileRow
	Max     int    // show at most this many, then "… N more <More>"; 0 = all
	More    string // the noun for the rest, such as "files"
	MoreOne string // the noun for one more, such as "file" (More if empty)
}

// Files writes a file list. Rows in the heading's root show paths relative to it; rows in other roots come under a
// sub-heading per root, also relative. Columns line up; a path never is cut: if a row does not fit the terminal's
// width, its path goes on its own line below the columns.
func (r *Renderer) Files(l FileList) {
	rows := l.Rows
	more := 0
	if l.Max > 0 && len(rows) > l.Max {
		rows, more = rows[:l.Max], len(rows)-l.Max
	}
	// Column widths over the rows shown and the headers.
	widths := make([]int, len(l.Columns))
	headed := false
	for i, c := range l.Columns {
		widths[i] = width(plain(c.Header))
		headed = headed || c.Header != ""
	}
	for _, row := range rows {
		for i := range l.Columns {
			if i < len(row.Cells) {
				widths[i] = max(widths[i], width(plain(row.Cells[i])))
			}
		}
	}
	cells := func(values []string, style string) []span {
		var out []span
		for i, c := range l.Columns {
			v := ""
			if i < len(values) {
				v = values[i]
			}
			pad := widths[i] - width(plain(v))
			if i > 0 {
				out = append(out, spaces(3))
			}
			if c.Right {
				out = append(out, spaces(pad), styled(v, style))
			} else {
				out = append(out, styled(v, style), spaces(pad))
			}
		}
		return out
	}
	indent := 4
	group := func(root string) {
		if root != r.root || r.root == "" {
			r.write(spaces(2), styled(r.Tilde(root), bold))
		}
	}
	if headed {
		headers := make([]string, len(l.Columns))
		for i, c := range l.Columns {
			headers[i] = c.Header
		}
		r.write(append([]span{spaces(indent)}, cells(headers, dim)...)...)
	}
	lastRoot := ""
	for i, row := range rows {
		if i == 0 || row.Root != lastRoot {
			group(row.Root)
			lastRoot = row.Root
		}
		path := relName(row.Rel) // relative to the heading's root, or to its root's sub-heading
		line := append([]span{spaces(indent)}, cells(row.Cells, "")...)
		var tail []span
		if len(l.Columns) > 0 {
			tail = append(tail, spaces(3))
		}
		tail = append(tail, name(path))
		if row.Note != "" {
			tail = append(tail, spaces(3), styled(row.Note, dim))
		}
		if r.o.Width > 0 && width(append(line, tail...)...) > r.o.Width && len(l.Columns) > 0 {
			r.write(line...)
			r.write(append([]span{spaces(indent + 2)}, tail[1:]...)...)
			continue
		}
		r.write(append(line, tail...)...)
	}
	if more > 0 {
		one := l.MoreOne
		if one == "" {
			one = l.More
		}
		r.more(more, one, l.More)
	}
}

// more writes "… N more files" under a cut list.
func (r *Renderer) more(n int, one, many string) {
	noun := many
	if n == 1 {
		noun = one
	}
	r.write(spaces(4), styled(r.ellipsis()+" "+Count(int64(n))+" more "+noun, dim))
}

// ellipsis is "…" in colour, "..." plain.
func (r *Renderer) ellipsis() string { return r.pick("…", "...") }
