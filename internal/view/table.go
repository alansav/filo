package view

// TableRow is one row of a table: a name (shown as a name, so a trailing space shows), its cells, and an optional
// note that needs attention, shown last under the ! marker.
type TableRow struct {
	Name      string
	Cells     []string
	Attention string
}

// TableGroup is a heading name (a directory) with its rows under it, and an optional note that needs attention on
// the heading's line.
type TableGroup struct {
	Name      string
	Attention string
	Rows      []TableRow
}

// Table writes rows with their columns lined up, indented by two: the name, then each cell (right-aligned where
// right says so), then the attention note. It is for a short list of named things (policies), not files.
func (r *Renderer) Table(rows []TableRow, right ...bool) {
	l := newLayout(rows)
	for _, row := range rows {
		r.row(l, 2, row, right)
	}
}

// Groups writes each group's name, indented by two, and its rows under it, indented by four, with the columns
// lined up across every group, and each attention note at the end of its line (a group's lined up with its rows').
// It is for a short list of named things in places (directory rules), not files.
func (r *Renderer) Groups(groups []TableGroup, right ...bool) {
	var all []TableRow
	for _, g := range groups {
		all = append(all, g.Rows...)
	}
	l := newLayout(all)
	for _, g := range groups {
		spans := []span{spaces(2), name(g.Name)}
		if g.Attention != "" {
			// The note goes where the rows' notes go: after the name column and every cell, from four in.
			pad := 4 + l.nameWidth + l.cellsWidth() - 2 - width(name(g.Name))
			spans = append(spans, spaces(max(pad, 0)+3), r.mark(Attention), plain(" "+g.Attention))
		}
		r.write(spans...)
		for _, row := range g.Rows {
			r.row(l, 4, row, right)
		}
	}
}

// HeadedTable writes rows as Table does, but indented by four, as a list under a section, below a dimmed line of
// headers: the name column's first, then one per cell column (fewer headers leave the last columns without one).
func (r *Renderer) HeadedTable(headers []string, rows []TableRow, right ...bool) {
	l := newLayout(rows)
	if len(headers) > 0 {
		l.nameWidth = max(l.nameWidth, width(plain(headers[0])))
	}
	for i := range l.widths {
		if i+1 < len(headers) {
			l.widths[i] = max(l.widths[i], width(plain(headers[i+1])))
		}
	}
	if len(headers) > 0 {
		spans := []span{spaces(4), styled(headers[0], dim), spaces(l.nameWidth - width(plain(headers[0])))}
		for i, h := range headers[1:] {
			if i >= len(l.widths) {
				break
			}
			spans = append(spans, spaces(3), styled(h, dim), spaces(l.widths[i]-width(plain(h))))
		}
		r.write(spans...)
	}
	for _, row := range rows {
		r.row(l, 4, row, right)
	}
}

// layout is the widths a table lines its columns up to.
type layout struct {
	nameWidth int
	widths    []int
}

func newLayout(rows []TableRow) layout {
	var l layout
	cols := 0
	for _, row := range rows {
		l.nameWidth = max(l.nameWidth, width(name(row.Name)))
		cols = max(cols, len(row.Cells))
	}
	l.widths = make([]int, cols)
	for _, row := range rows {
		for i, c := range row.Cells {
			l.widths[i] = max(l.widths[i], width(plain(c)))
		}
	}
	return l
}

// cellsWidth is the width of every cell column with the three spaces before each.
func (l layout) cellsWidth() int {
	n := 0
	for _, w := range l.widths {
		n += 3 + w
	}
	return n
}

// row writes one row of a table, indented by indent.
func (r *Renderer) row(l layout, indent int, row TableRow, right []bool) {
	spans := []span{spaces(indent), name(row.Name), spaces(l.nameWidth - width(name(row.Name)))}
	cols := len(l.widths) // up to the last cell with text, unless a note comes after them: no trailing spaces
	if row.Attention == "" {
		for cols > 0 && (cols > len(row.Cells) || row.Cells[cols-1] == "") {
			cols--
		}
	}
	for i := range cols {
		v := ""
		if i < len(row.Cells) {
			v = row.Cells[i]
		}
		pad := l.widths[i] - width(plain(v))
		spans = append(spans, spaces(3))
		if i < len(right) && right[i] {
			spans = append(spans, spaces(pad), plain(v))
		} else {
			spans = append(spans, plain(v), spaces(pad))
		}
	}
	if row.Attention != "" {
		spans = append(spans, spaces(3), r.mark(Attention), plain(" "+row.Attention))
	}
	r.write(spans...)
}
