// Package tagcsv writes and reads the tags CSV (docs/design.md, Tags, "The tags CSV"): every present file with its
// tags in one cell, separated by "; ", which the update reads back as the file's tags. The register side (listing
// files, recording exports, applying updates) is in package store; the spelling rules are package tags'.
package tagcsv

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
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
// none of a retention or duplicates CSV's own (file_date, retention, group).
func LostTags(data []byte) bool {
	sh, _ := sheet.Open(data, "id", "export", "directory", "name", "modified")
	return sh != nil && !sh.Has("tags") && !sh.Has("file_date") && !sh.Has("retention") && !sh.Has("group")
}

// WriteCSV writes items as one export, each with its tags cell. Times are shown in loc.
func WriteCSV(w io.Writer, exportID int64, items []store.TagItem, loc *time.Location) error {
	if _, err := io.WriteString(w, sheet.BOM); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(Header); err != nil {
		return err
	}
	ex := strconv.FormatInt(exportID, 10)
	for _, it := range items {
		dir, name := path.Split(it.Rel)
		directory := it.Root
		if dir = strings.TrimSuffix(dir, "/"); dir != "" {
			directory = it.Root + "/" + dir
		}
		rec := []string{
			strconv.FormatInt(it.FileID, 10), ex, sheet.Cell(directory), sheet.Cell(name),
			strconv.FormatInt(it.Size, 10), time.Unix(0, it.MtimeNS).In(loc).Format("2006-01-02 15:04"),
			sheet.Cell(store.JoinTags(it.Tags)),
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// Problem is a row the update refuses before it reaches the register.
type Problem struct {
	Line int
	Msg  string
}

// Parsed is what ParseCSV found.
type Parsed struct {
	Rows     []store.TagRow // every readable row; the register decides which changed
	Problems []Problem
}

// ParseCSV reads an edited tags CSV already read (with sheet.ReadFile), as spreadsheets save it (see package sheet).
// Each cell is read with the spelling rules: tags separated by ";", spaces around them dropped, a tag typed twice
// (in any case) one, an empty cell no tags. A row with a tag the rules refuse is refused whole, and so is a row
// listed more than once, every time it appears.
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
			p.Problems = append(p.Problems, Problem{row.Line, fmt.Sprintf(format, args...)})
		}
		id, idErr := strconv.ParseInt(row.Get("id"), 10, 64)
		ex, exErr := strconv.ParseInt(row.Get("export"), 10, 64)
		switch {
		case idErr != nil || id < 1:
			bad("id %q is not a file ID", row.Get("id"))
			return
		case exErr != nil || ex < 1:
			bad("export %q is not an export number", row.Get("export"))
			return
		}
		if v := row.Beyond(); v != "" {
			bad("%q is outside the columns; put it under tags", v)
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
	p.Rows = sheet.DropRepeated(p.Rows, func(r store.TagRow) [2]int64 { return [2]int64{r.ExportID, r.FileID} },
		func(r store.TagRow) {
			p.Problems = append(p.Problems, Problem{r.Line,
				fmt.Sprintf("file %d of export %d is listed more than once", r.FileID, r.ExportID)})
		})
	return p, nil
}

// ParseCell reads a tags cell: the tags separated by ";", each once. The apostrophe sheet.Cell puts before a cell
// that a spreadsheet would read as a formula (=, +, -, @) is dropped first.
func ParseCell(cell string) ([]tags.Tag, error) {
	cell = strings.TrimSpace(cell)
	if len(cell) > 1 && cell[0] == '\'' && strings.ContainsRune("=+-@", rune(cell[1])) {
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
