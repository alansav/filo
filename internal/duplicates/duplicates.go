// Package duplicates reads and writes the duplicates CSV: every copy of each duplicate group, with a delete cell in
// which the person writes Y or N (docs/design.md, Duplicates). The register side (finding groups, recording exports,
// checking and recording decisions) is in package store; writing a cell safely is shared with package retention.
package duplicates

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/alansav/filo/internal/retention"
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
	_, cols, _, err := header(data)
	if err != nil {
		return false
	}
	_, del := cols["delete"]
	_, ret := cols["retention"]
	return del || !ret
}

// WriteCSV writes groups as one export, numbered from 1 in the order given, one row per copy. Every delete cell is
// filled in with prefill (an export of separate contents fills in N), or left blank. Times are shown in loc.
func WriteCSV(w io.Writer, exportID int64, groups []store.DupContent, prefill string, loc *time.Location) error {
	if _, err := io.WriteString(w, retention.BOM); err != nil {
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
				strconv.FormatInt(c.FileID, 10), ex, group, copies, retention.Cell(directory), retention.Cell(name),
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

// header finds the separator and the columns, as spreadsheets save them: ',', ';' or tab.
func header(data []byte) (*csv.Reader, map[string]int, int, error) {
	data = bytes.TrimPrefix(data, []byte(retention.BOM))
	for _, sep := range []rune{',', ';', '\t'} {
		c := csv.NewReader(bytes.NewReader(data))
		c.Comma = sep
		c.FieldsPerRecord = -1
		head, err := c.Read()
		if err != nil {
			continue
		}
		m := map[string]int{}
		for i, h := range head {
			name := strings.ToLower(strings.TrimSpace(h))
			if _, dup := m[name]; !dup {
				m[name] = i
			}
		}
		if hasAll(m, "id", "export", "group") {
			return c, m, len(head), nil
		}
	}
	return nil, nil, 0, ErrNotDuplicatesCSV
}

// ReadCSV reads an edited duplicates CSV. Like the retention CSV it accepts a byte order mark, CRLF line ends, ';' or
// tab separators, columns moved or added, and empty rows. A row listed more than once is refused every time.
func ReadCSV(r io.Reader) (Parsed, error) {
	var p Parsed
	data, err := io.ReadAll(r)
	if err != nil {
		return p, err
	}
	cr, cols, width, err := header(data)
	if err != nil {
		return p, err
	}
	if _, ok := cols["delete"]; !ok {
		return p, ErrNoDelete
	}
	seen := map[[2]int64][]int{} // (export, id) -> indexes into p.Rows
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return p, err
		}
		line, _ := cr.FieldPos(0)
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		get := func(name string) string {
			i, ok := cols[name]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		bad := func(format string, args ...any) {
			p.Problems = append(p.Problems, retention.Problem{Line: line, Msg: fmt.Sprintf(format, args...)})
		}
		id, idErr := strconv.ParseInt(get("id"), 10, 64)
		ex, exErr := strconv.ParseInt(get("export"), 10, 64)
		group, gErr := strconv.ParseInt(get("group"), 10, 64)
		switch {
		case idErr != nil || id < 1:
			bad("id %q is not a file ID", get("id"))
			continue
		case exErr != nil || ex < 1:
			bad("export %q is not an export number", get("export"))
			continue
		case gErr != nil || group < 1:
			bad("group %q is not a group number", get("group"))
			continue
		}
		if v := beyond(rec, width); v != "" {
			bad("%q is outside the columns; put Y or N under delete", v)
			continue
		}
		d, err := ParseDecision(get("delete"))
		if err != nil {
			bad("%v", err)
			continue
		}
		key := [2]int64{ex, id}
		seen[key] = append(seen[key], len(p.Rows))
		p.Rows = append(p.Rows, store.DupRow{Line: line, ExportID: ex, FileID: id, Group: group, Decision: d})
	}

	drop := map[int]bool{}
	for _, idx := range seen {
		if len(idx) > 1 {
			for _, i := range idx {
				drop[i] = true
				r := p.Rows[i]
				p.Problems = append(p.Problems, retention.Problem{Line: r.Line,
					Msg: fmt.Sprintf("file %d of export %d is listed more than once", r.FileID, r.ExportID)})
			}
		}
	}
	if len(drop) > 0 {
		kept := p.Rows[:0]
		for i, r := range p.Rows {
			if !drop[i] {
				kept = append(kept, r)
			}
		}
		p.Rows = kept
	}
	return p, nil
}

// beyond returns the first non-empty value past the header's columns, or "".
func beyond(rec []string, width int) string {
	for i := width; i < len(rec); i++ {
		if v := strings.TrimSpace(rec[i]); v != "" {
			return v
		}
	}
	return ""
}

func hasAll(m map[string]int, names ...string) bool {
	for _, n := range names {
		if _, ok := m[n]; !ok {
			return false
		}
	}
	return true
}
