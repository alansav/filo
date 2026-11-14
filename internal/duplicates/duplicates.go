// Package duplicates reads and writes the duplicates CSV: every copy of each duplicate group, with a delete cell in
// which the person writes Y or N (docs/design.md, Duplicates). The register side (finding groups, recording exports,
// checking and recording decisions) is in package store; what spreadsheets do to a CSV is handled in package sheet.
package duplicates

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/store"
)

// Header is the CSV's columns, in order. The update finds columns by name, so a spreadsheet may reorder them or add
// its own; it reads only id, export, group and delete.
var Header = []string{"id", "export", "group", "copies", "directory", "name", "size", "modified", "file_date",
	"retention", "keep_until", "delete"}

var (
	// ErrNotDuplicatesCSV means the header does not have the columns of a duplicates CSV.
	ErrNotDuplicatesCSV = errors.New("not a filo duplicates CSV: the header needs id, export, group and delete columns")
	// ErrNoDelete means a duplicates CSV lost its delete column (deleted or renamed in the spreadsheet).
	ErrNoDelete = errors.New("the CSV has no delete column, which filo update needs; put it back with that name, " +
		"or export again")
)

// IsDuplicatesCSV reports whether data's header names a duplicates CSV, so filo update can tell it from a retention
// CSV: it has a group and a delete column, or a group column and no retention column (a duplicates CSV whose delete
// column was lost, which ReadCSV then explains). A retention CSV with a group column of the person's own stays a
// retention CSV.
func IsDuplicatesCSV(data []byte) bool {
	sh, _ := sheet.Open(data, "id", "export", "group")
	return sh != nil && (sh.Has("delete") || !sh.Has("retention"))
}

// WriteCSV writes groups as one export, numbered from 1 in the order given, one row per copy. Every delete cell is
// filled in with prefill (an export of separate contents fills in N), or left blank. Times are shown in loc.
func WriteCSV(w io.Writer, exportID int64, groups []store.DupContent, prefill string, loc *time.Location) error {
	if _, err := io.WriteString(w, sheet.BOM); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(Header); err != nil {
		return err
	}
	ex := strconv.FormatInt(exportID, 10)
	for i, g := range groups {
		group := strconv.Itoa(i + 1)
		copies := strconv.Itoa(len(g.Copies))
		for _, c := range g.Copies {
			dir, name := path.Split(c.Path)
			directory := c.RootPath
			if dir = strings.TrimSuffix(dir, "/"); dir != "" {
				directory = c.RootPath + "/" + dir
			}
			rec := []string{
				strconv.FormatInt(c.FileID, 10), ex, group, copies, sheet.Cell(directory), sheet.Cell(name),
				strconv.FormatInt(c.Size, 10), time.Unix(0, c.MtimeNS).In(loc).Format("2006-01-02 15:04"),
				c.FileDate, c.Retention, c.KeepUntil, prefill,
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

// Parsed is what ReadCSV found.
type Parsed struct {
	Rows     []store.DupRow
	Problems []retention.Problem // rows refused before they reach the register
}

// ParseDecision reads a delete cell: Y or yes, N or no (any case), or blank. Anything else is refused, never guessed.
func ParseDecision(s string) (byte, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return store.DecideNone, nil
	case "y", "yes":
		return store.DecideDelete, nil
	case "n", "no":
		return store.DecideKeep, nil
	}
	return 0, fmt.Errorf("delete %q: write Y to delete this copy or N to keep it, or leave it blank", s)
}

// ReadCSV reads an edited duplicates CSV, as spreadsheets save it (see package sheet). A row listed more than once is
// refused every time.
func ReadCSV(r io.Reader) (Parsed, error) {
	var p Parsed
	data, err := io.ReadAll(r)
	if err != nil {
		return p, err
	}
	sh, _ := sheet.Open(data, "id", "export", "group")
	switch {
	case sh == nil:
		return p, ErrNotDuplicatesCSV
	case !sh.Has("delete"):
		return p, ErrNoDelete
	}
	err = sh.Each(func(row sheet.Row) {
		bad := func(format string, args ...any) {
			p.Problems = append(p.Problems, retention.Problem{Line: row.Line, Msg: fmt.Sprintf(format, args...)})
		}
		id, idErr := strconv.ParseInt(row.Get("id"), 10, 64)
		ex, exErr := strconv.ParseInt(row.Get("export"), 10, 64)
		group, gErr := strconv.ParseInt(row.Get("group"), 10, 64)
		switch {
		case idErr != nil || id < 1:
			bad("id %q is not a file ID", row.Get("id"))
			return
		case exErr != nil || ex < 1:
			bad("export %q is not an export number", row.Get("export"))
			return
		case gErr != nil || group < 1:
			bad("group %q is not a group number", row.Get("group"))
			return
		}
		if v := row.Beyond(); v != "" {
			bad("%q is outside the columns; put Y or N under delete", v)
			return
		}
		d, err := ParseDecision(row.Get("delete"))
		if err != nil {
			bad("%v", err)
			return
		}
		p.Rows = append(p.Rows, store.DupRow{Line: row.Line, ExportID: ex, FileID: id, Group: group, Decision: d})
	})
	if err != nil {
		return p, err
	}
	p.Rows = sheet.DropRepeated(p.Rows, func(r store.DupRow) [2]int64 { return [2]int64{r.ExportID, r.FileID} },
		func(r store.DupRow) {
			p.Problems = append(p.Problems, retention.Problem{Line: r.Line,
				Msg: fmt.Sprintf("file %d of export %d is listed more than once", r.FileID, r.ExportID)})
		})
	return p, nil
}
