package view

// TableRow is one row of a table: a name (shown as a name, so a trailing space shows), its cells, and an optional
// note that needs attention, shown last under the ! marker.
type TableRow struct {
	Name      string
	Cells     []string
	Attention string
}

// Table writes rows with their columns lined up, indented by two: the name, then each cell (right-aligned where
// right says so), then the attention note. It is for a short list of named things (policies), not files.
func (r *Renderer) Table(rows []TableRow, right ...bool) {
	nameWidth, cols := 0, 0
	for _, row := range rows {
		nameWidth = max(nameWidth, width(name(row.Name)))
		cols = max(cols, len(row.Cells))
	}
	widths := make([]int, cols)
	for _, row := range rows {
		for i, c := range row.Cells {
			widths[i] = max(widths[i], width(plain(c)))
		}
	}
	for _, row := range rows {
		spans := []span{spaces(2), name(row.Name), spaces(nameWidth - width(name(row.Name)))}
		for i := 0; i < cols; i++ {
			v := ""
			if i < len(row.Cells) {
				v = row.Cells[i]
			}
			pad := widths[i] - width(plain(v))
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
}
