// Package filecsv writes and reads the combined CSV (docs/design.md, Tags, "The combined CSV"): every present file
// with its file_date, retention, keep_until and tags, so both are edited in one pass. Its retention half is read as
// package retention reads a retention CSV and its tags cell as package tagcsv reads one; the register side (listing
// files, recording exports, applying updates) is in package store.
package filecsv

import (
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tagcsv"
)

// Header is the CSV's columns, in order: the retention CSV's, then tags. The update finds columns by name, so a
// spreadsheet may reorder them or add its own; it reads id, export, file_date, retention and tags, and checks that
// keep_until was left alone.
var Header = append(append([]string{}, retention.Header...), "tags")

// ErrNotFilesCSV means the header lacks the columns the update needs.
var ErrNotFilesCSV = errors.New("not a filo CSV: the header needs id, export, retention and tags columns")

// IsFilesCSV reports whether data's header has the columns of a combined CSV: id, export, retention and tags. A
// retention CSV given a tags column of the person's own has them too: which it is, the exports its rows name say.
func IsFilesCSV(data []byte) bool {
	sh, _ := sheet.Open(data, "id", "export", "retention", "tags")
	return sh != nil && !sh.Has("group")
}

// LostColumn reports, for a CSV that is not a combined one, the column it lacks if it has all the others the update
// reads (id, export, and retention or tags), with every export its rows name, each once; missing is "" otherwise.
// The caller asks the register whether those exports are combined ones: a combined CSV that lost its retention or
// tags column (deleted or renamed in the spreadsheet) is refused naming it, since a lost tags column would otherwise
// read as every tag removed (docs/design.md, Tags, "The combined CSV").
func LostColumn(data []byte) (missing string, exports []int64) {
	sh, _ := sheet.Open(data, "id", "export")
	if sh == nil || sh.Has("group") || sh.Has("retention") == sh.Has("tags") {
		return "", nil
	}
	missing = "tags"
	if sh.Has("tags") {
		missing = "retention"
	}
	_ = sh.Each(func(row sheet.Row) { // a broken quote ends the list: the exports read so far still say what it is
		if _, ex, problem := sheet.ReadIDs(row, missing); problem == "" && !slices.Contains(exports, ex) {
			exports = append(exports, ex)
		}
	})
	return missing, exports
}

// NameCell is a file name as the combined CSV writes it: as sheet.Cell writes any display cell (valid UTF-8, an
// apostrophe before one a spreadsheet would read as a formula), and an apostrophe before a name that starts with one
// itself, so that ReadName, dropping one, always gives back the name as it is (docs/design.md, Tags, "Renaming a
// file by its name cell").
func NameCell(name string) string {
	if strings.HasPrefix(name, "'") {
		return "'" + strings.ToValidUTF8(name, "\uFFFD")
	}
	return sheet.Cell(name)
}

// ReadName is the file name an edited name cell asks for: the cell with the apostrophe NameCell puts before a
// formula character or another apostrophe dropped. Nothing else is changed, spaces included.
func ReadName(cell string) string {
	if len(cell) > 1 && cell[0] == '\'' && strings.ContainsRune("=+-@\t\r'", rune(cell[1])) {
		return cell[1:]
	}
	return cell
}

// SetNameCells fills in each item's name cell, as WriteCSV writes it, so the export records the cell the CSV shows.
func SetNameCells(items []store.FilesItem) {
	for i := range items {
		items[i].NameCell = NameCell(path.Base(items[i].Path))
	}
}

// WriteCSV writes items as one export: the file_date, retention and keep_until they carry (the caller fills in
// file_date from the name first, and records the same values with the export), and each one's tags cell. Times are
// shown in loc. The name cell is the item's NameCell (SetNameCells), so the CSV holds what the export recorded.
func WriteCSV(w io.Writer, exportID int64, items []store.FilesItem, loc *time.Location) error {
	cw, err := sheet.NewWriter(w, Header)
	if err != nil {
		return err
	}
	ex := strconv.FormatInt(exportID, 10)
	for _, it := range items {
		cells := sheet.FileCells(it.RootPath, it.Path, it.Size, it.MtimeNS, loc)
		cells[1] = it.NameCell
		if cells[1] == "" {
			cells[1] = NameCell(path.Base(it.Path))
		}
		rec := append([]string{strconv.FormatInt(it.FileID, 10), ex}, cells...)
		rec = append(rec, it.FileDate, it.Retention, it.KeepUntil, tagcsv.GuardCell(store.JoinTags(it.Tags)))
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// Parsed is what ParseCSV found.
type Parsed struct {
	Rows     []store.FilesRow // every readable row; the register decides which changed
	Problems []sheet.Problem
	Exports  []int64 // every export a row names, refused rows included, each once
}

// ParseCSV reads an edited combined CSV already read (with sheet.ReadFile), as spreadsheets save it (see package
// sheet). A row is refused whole if either half cannot be read, and so is a file listed more than once (under one
// export or several), every time it appears, even where another of its rows is refused for its cells.
func ParseCSV(data []byte) (Parsed, error) {
	var p Parsed
	if len(data) > sheet.MaxSize {
		return p, sheet.ErrTooLarge
	}
	sh, _ := sheet.Open(data, "id", "export", "retention", "tags")
	if sh == nil {
		return p, ErrNotFilesCSV
	}
	listed := map[int64]int{} // file -> the rows naming it, refused or not
	err := sh.Each(func(row sheet.Row) {
		bad := func(msg string) { p.Problems = append(p.Problems, sheet.Problem{Line: row.Line, Msg: msg}) }
		id, ex, problem := sheet.ReadIDs(row, "file_date, retention or tags")
		if problem != "" {
			bad(problem)
			return
		}
		listed[id]++
		if !slices.Contains(p.Exports, ex) {
			p.Exports = append(p.Exports, ex)
		}
		rr, problem := retention.ReadRow(sh, row, id, ex)
		if problem != "" {
			bad(problem)
			return
		}
		ts, err := tagcsv.ParseCell(row.Get("tags"))
		if err != nil {
			bad(fmt.Sprintf("tags %v", err))
			return
		}
		fr := store.FilesRow{RetentionRow: rr, Tags: ts}
		if sh.Has("name") {
			fr.HasName, fr.NameCell = true, row.Raw("name") // spaces are part of a name
			fr.Name = ReadName(fr.NameCell)
		}
		if sh.Has("directory") {
			fr.HasDirectory, fr.DirectoryCell = true, row.Raw("directory") // spaces are part of a directory's name
		}
		p.Rows = append(p.Rows, fr)
	})
	if err != nil {
		return p, err // a broken quote: csv says where
	}
	// One file, one row: a file under two exports in one CSV would be changed by both, each checked against what it
	// had before either.
	kept := p.Rows[:0]
	for _, r := range p.Rows {
		if listed[r.FileID] > 1 {
			p.Problems = append(p.Problems, sheet.Problem{Line: r.Line,
				Msg: fmt.Sprintf("file %d is listed more than once", r.FileID)})
			continue
		}
		kept = append(kept, r)
	}
	p.Rows = kept
	slices.SortStableFunc(p.Problems, func(a, b sheet.Problem) int { return a.Line - b.Line })
	return p, nil
}
