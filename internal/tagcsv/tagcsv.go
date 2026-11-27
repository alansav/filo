// Package tagcsv writes and reads the tags CSV (docs/design.md, Tags, "The tags CSV"): every present file with its
// tags in one cell, separated by "; ", which the update reads back as the file's tags. The register side (listing
// files, recording exports, applying updates) is in package store; the spelling rules are package tags'.
package tagcsv

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

// Header is the CSV's columns, in order. The update finds columns by name, so a spreadsheet may reorder them or add
// its own; it reads only id, export and tags.
var Header = []string{"id", "export", "directory", "name", "size", "modified", "tags"}

var (
	// ErrNotTagsCSV means the header lacks the columns the update needs.
	ErrNotTagsCSV = errors.New("not a filo tags CSV: the header needs id, export and tags columns")
	// ErrNoTags means a tags CSV lost its tags column (deleted or renamed in the spreadsheet).
	ErrNoTags = errors.New("the CSV has no tags column, which filo update needs; put it back with that name, or " +
		"export again")
)

// IsTagsCSV reports whether data's header names a tags CSV: id, export and tags columns, and neither a retention
// column nor a group column (a retention or duplicates CSV with a tags column of the person's own stays what it is).
func IsTagsCSV(data []byte) bool {
	sh, _ := sheet.Open(data, "id", "export", "tags")
	return sh != nil && !sh.Has("retention") && !sh.Has("group")
}

// LostTags reports whether data looks like a tags CSV whose tags column was lost: the columns of one but tags, and
// none of a retention or duplicates CSV's own, of this design or an older one (file_date, retention, keep_until,
// review_date, group).
func LostTags(data []byte) bool {
	sh, _ := sheet.Open(data, "id", "export", "directory", "name", "modified")
	if sh == nil || sh.Has("tags") {
		return false
	}
	for _, other := range []string{"file_date", "retention", "keep_until", "review_date", "group"} {
		if sh.Has(other) {
			return false
		}
	}
	return true
}

// WriteCSV writes items as one export, each with its tags cell. Times are shown in loc.
func WriteCSV(w io.Writer, exportID int64, items []store.TagItem, loc *time.Location) error {
	cw, err := sheet.NewWriter(w, Header)
	if err != nil {
		return err
	}
	ex := strconv.FormatInt(exportID, 10)
	for _, it := range items {
		rec := append([]string{strconv.FormatInt(it.FileID, 10), ex},
			sheet.FileCells(it.Root, it.Rel, it.Size, it.MtimeNS, loc)...)
		rec = append(rec, GuardCell(store.JoinTags(it.Tags)))
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// Parsed is what ParseCSV found.
type Parsed struct {
	Rows     []store.TagRow // every readable row; the register decides which changed
	Problems []sheet.Problem
}

// ParseCSV reads an edited tags CSV already read (with sheet.ReadFile), as spreadsheets save it (see package sheet).
// Each cell is read with the spelling rules: tags separated by ";", spaces around them dropped, a tag typed twice
// (in any case) one, an empty cell no tags. A row with a tag the rules refuse is refused whole, and so is a file
// listed more than once (under one export or several), every time it appears.
func ParseCSV(data []byte) (Parsed, error) {
	var p Parsed
	if len(data) > sheet.MaxSize {
		return p, sheet.ErrTooLarge
	}
	sh, _ := sheet.Open(data, "id", "export", "tags")
	if sh == nil {
		if LostTags(data) {
			return p, ErrNoTags
		}
		return p, ErrNotTagsCSV
	}
	err := sh.Each(func(row sheet.Row) {
		bad := func(format string, args ...any) {
			p.Problems = append(p.Problems, sheet.Problem{Line: row.Line, Msg: fmt.Sprintf(format, args...)})
		}
		id, ex, problem := sheet.ReadIDs(row, "tags")
		if problem != "" {
			bad("%s", problem)
			return
		}
		ts, err := ParseCell(row.Get("tags"))
		if err != nil {
			bad("tags %v", err)
			return
		}
		p.Rows = append(p.Rows, store.TagRow{Line: row.Line, ExportID: ex, FileID: id, Tags: ts})
	})
	if err != nil {
		return p, err // a broken quote: csv says where
	}
	// One file, one row: a file under two exports in one CSV would be changed by both, each checked against the tags
	// it had before either.
	p.Rows = sheet.DropRepeated(p.Rows, func(r store.TagRow) int64 { return r.FileID },
		func(r store.TagRow) {
			p.Problems = append(p.Problems, sheet.Problem{Line: r.Line,
				Msg: fmt.Sprintf("file %d is listed more than once", r.FileID)})
		})
	return p, nil
}

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
