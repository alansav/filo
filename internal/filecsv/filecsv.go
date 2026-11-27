// Package filecsv writes and reads the combined CSV (docs/design.md, Tags, "The combined CSV"): every present file
// with its file_date, retention, keep_until and tags, so both are edited in one pass. Its retention half is read as
// package retention reads a retention CSV and its tags cell as package tagcsv reads one; the register side (listing
// files, recording exports, applying updates) is in package store.
package filecsv

import (
	"errors"
	"fmt"
	"io"
	"strconv"
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

// WriteCSV writes items as one export: the file_date, retention and keep_until they carry (the caller fills in
// file_date from the name first, and records the same values with the export), and each one's tags cell. Times are
// shown in loc.
func WriteCSV(w io.Writer, exportID int64, items []store.FilesItem, loc *time.Location) error {
	cw, err := sheet.NewWriter(w, Header)
	if err != nil {
		return err
	}
	ex := strconv.FormatInt(exportID, 10)
	for _, it := range items {
		rec := append([]string{strconv.FormatInt(it.FileID, 10), ex},
			sheet.FileCells(it.RootPath, it.Path, it.Size, it.MtimeNS, loc)...)
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
}

// ParseCSV reads an edited combined CSV already read (with sheet.ReadFile), as spreadsheets save it (see package
// sheet). A row is refused whole if either half cannot be read, and so is a file listed more than once (under one
// export or several), every time it appears.
func ParseCSV(data []byte) (Parsed, error) {
	var p Parsed
	if len(data) > sheet.MaxSize {
		return p, sheet.ErrTooLarge
	}
	sh, _ := sheet.Open(data, "id", "export", "retention", "tags")
	if sh == nil {
		return p, ErrNotFilesCSV
	}
	err := sh.Each(func(row sheet.Row) {
		bad := func(msg string) { p.Problems = append(p.Problems, sheet.Problem{Line: row.Line, Msg: msg}) }
		id, ex, problem := sheet.ReadIDs(row, "file_date, retention or tags")
		if problem != "" {
			bad(problem)
			return
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
		p.Rows = append(p.Rows, store.FilesRow{RetentionRow: rr, Tags: ts})
	})
	if err != nil {
		return p, err // a broken quote: csv says where
	}
	// One file, one row: a file under two exports in one CSV would be changed by both, each checked against what it
	// had before either.
	p.Rows = sheet.DropRepeated(p.Rows, func(r store.FilesRow) int64 { return r.FileID }, func(r store.FilesRow) {
		p.Problems = append(p.Problems, sheet.Problem{Line: r.Line,
			Msg: fmt.Sprintf("file %d is listed more than once", r.FileID)})
	})
	return p, nil
}
