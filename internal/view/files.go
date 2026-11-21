package view

import "slices"

// Column is one fixed column of a file list, before the path: a date, a size, a retention.
type Column struct {
	Header string // shown above the column when any column has one
	Right  bool   // right-aligned (sizes)
}

// FileRow is one file in a list: where it is, its cells for the columns, and an optional note after the path.
type FileRow struct {
	Root, Rel string // the root, absolute, and the path relative to it ('/' separators)
	From      string // a move's earlier path: shown as "from -> Rel"
	FromRoot  string // the root From is relative to, when not the row's own (a file back from another root): in full
	Label     string // in place of a path, for a row that has none: shown as "(Label)", never as a path
	Cells     []string
	Note      string
}

// Over is a header over several columns of a file list, on a line above theirs: "file_date" over "was" and "now".
type Over struct {
	Header  string
	Columns int // how many columns it spans, from the one after the previous Over
}

// FileList is a list of files under a section.
type FileList struct {
	Columns []Column
	Over    []Over // headers over groups of columns, on a line above the columns' own; none = no such line
	Rows    []FileRow
	ByRoot  bool   // the rows are ordered by root (then path): sub-headings per root; never guessed from the rows
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
	// Sub-headings, one per root, only for a list its caller ordered by root (ByRoot), whose rows all have a root,
	// with some row outside the heading's root: then every root gets one, the heading's too, so no row reads as part
	// of the root above it. Any other list (sorted by size or time) has no sub-headings: a row in the heading's root
	// is relative, any other shows its full path. The component never guesses the order from the rows.
	grouped := l.ByRoot
	var roots []string
	for _, row := range rows {
		if row.Root == "" || row.Label != "" {
			grouped = false
			break
		}
		if !slices.Contains(roots, row.Root) {
			roots = append(roots, row.Root)
		}
	}
	grouped = grouped && (len(roots) > 1 || (len(roots) == 1 && roots[0] != r.root))
	group := func(root string) {
		if grouped {
			r.write(spaces(2), styledName(r.Tilde(root), bold))
		}
	}
	// where shows a row's path or paths: relative under a sub-heading, else as Path does.
	where := func(row FileRow) []span {
		if row.Label != "" {
			return []span{styled("("+row.Label+")", dim)}
		}
		show := func(rel string) string {
			if grouped {
				return relName(rel)
			}
			return r.Path(row.Root, rel)
		}
		if row.From != "" {
			from := show(row.From)
			if row.FromRoot != "" && row.FromRoot != row.Root {
				from = r.Full(row.FromRoot, row.From) // another root: never under this one
			}
			return []span{name(from), plain(" -> "), name(show(row.Rel))}
		}
		return []span{name(show(row.Rel))}
	}
	if len(l.Over) > 0 {
		spans := []span{spaces(indent)}
		col := 0
		for i, o := range l.Over {
			w := 0 // the width the group's columns take, with the gaps between them
			for j := col; j < col+o.Columns && j < len(widths); j++ {
				if j > col {
					w += 3
				}
				w += widths[j]
			}
			if i > 0 {
				spans = append(spans, spaces(3))
			}
			spans = append(spans, styled(o.Header, dim), spaces(w-width(plain(o.Header))))
			col += o.Columns
		}
		r.write(spans...)
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
		line := append([]span{spaces(indent)}, cells(row.Cells, "")...)
		var tail []span
		if len(l.Columns) > 0 {
			tail = append(tail, spaces(3))
		}
		tail = append(tail, where(row)...)
		if row.Note != "" {
			tail = append(tail, spaces(3), styled(row.Note, dim))
		}
		// One column short of the width: some consoles wrap as soon as the last column is written.
		if r.o.Width > 0 && width(append(line, tail...)...) >= r.o.Width && len(l.Columns) > 0 {
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
		r.More(more, one, l.More)
	}
}

// More writes "… N more files" under a list that was cut, by a component or by whoever gathered it (a report's top
// entries).
func (r *Renderer) More(n int, one, many string) {
	noun := many
	if n == 1 {
		noun = one
	}
	r.write(spaces(4), styled(r.ellipsis()+" "+Count(int64(n))+" more "+noun, dim))
}

// ellipsis is "…" in colour, "..." plain.
func (r *Renderer) ellipsis() string { return r.pick("…", "...") }
