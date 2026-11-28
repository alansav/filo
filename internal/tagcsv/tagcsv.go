// Package tagcsv writes and reads the tags cell of the combined CSV (docs/design.md, Tags, "The combined CSV"): a
// file's tags in one cell, separated by "; ", which the update reads back as the file's tags. The CSV itself is
// package filecsv's; the spelling rules are package tags'.
package tagcsv

import (
	"strings"

	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/tags"
)

// GuardCell is a tags cell as written: sheet.Cell's apostrophe before a cell a spreadsheet would read as a formula
// (=, +, -, @), and one before a cell that starts with an apostrophe itself, so that ParseCell, dropping one, always
// gives back the tags as they are.
func GuardCell(cell string) string {
	if strings.HasPrefix(cell, "'") {
		return "'" + cell
	}
	return sheet.Cell(cell)
}

// ParseCell reads a tags cell: the tags separated by ";", each once. The apostrophe GuardCell puts before a cell is
// dropped first: one before =, +, -, @ or another apostrophe.
func ParseCell(cell string) ([]tags.Tag, error) {
	cell = strings.TrimSpace(cell)
	if len(cell) > 1 && cell[0] == '\'' && strings.ContainsRune("=+-@'", rune(cell[1])) {
		cell = cell[1:]
	}
	var out []tags.Tag
	for _, part := range strings.Split(cell, ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		t, err := tags.Parse(part)
		if err != nil {
			return nil, err
		}
		dup := false
		for _, o := range out {
			dup = dup || o.Same(t)
		}
		if !dup {
			out = append(out, t)
		}
	}
	return out, nil
}
