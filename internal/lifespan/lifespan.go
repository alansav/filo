// Package lifespan reads and writes the lifespan CSV: every present file with the date it is kept until. The register
// side (listing files, recording exports, applying updates) is in package store.
package lifespan

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

	"github.com/alansav/filo/internal/store"
)

// Header is the CSV's columns, in order. The update finds columns by name, so a spreadsheet may reorder them or add
// its own; it reads only id, export and keep_until.
var Header = []string{"id", "export", "directory", "name", "size", "modified", "keep_until"}

const dateLayout = "2006-01-02"

// bom marks the file as UTF-8 for spreadsheets that would otherwise guess another encoding.
const bom = "\ufeff"

// ParseDate checks a typed keep_until: "" stays "" (a blank cell), forever (in any case) is store.Forever, and
// YYYY-MM-DD must be a real date (2027-02-29 is not). Anything else is refused: a date like 06/07/2027 is ambiguous
// and is never guessed.
func ParseDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.EqualFold(s, store.Forever) {
		return store.Forever, nil
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return "", fmt.Errorf("%q is not a date: use YYYY-MM-DD, for example 2027-06-30, or forever (a spreadsheet "+
			"may have rewritten it; format the column as text)", s)
	}
	return t.Format(dateLayout), nil
}

// Today is t's date in its location, as YYYY-MM-DD.
func Today(t time.Time) string { return t.Format(dateLayout) }

// cell makes a value safe to put in a spreadsheet: valid UTF-8, and never read as a formula. Only display columns
// pass through here; the update never reads them.
func cell(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// WriteCSV writes items as one export. Times are shown in loc.
func WriteCSV(w io.Writer, exportID int64, items []store.FileItem, loc *time.Location) error {
	if _, err := io.WriteString(w, bom); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(Header); err != nil {
		return err
	}
	ex := strconv.FormatInt(exportID, 10)
	for _, it := range items {
		dir, name := path.Split(it.Path)
		directory := it.RootPath
		if dir = strings.TrimSuffix(dir, "/"); dir != "" {
			directory = it.RootPath + "/" + dir
		}
		rec := []string{
			strconv.FormatInt(it.FileID, 10), ex, cell(directory), cell(name), strconv.FormatInt(it.Size, 10),
			time.Unix(0, it.MtimeNS).In(loc).Format("2006-01-02 15:04"), it.KeepUntil,
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

// Parsed is what ReadCSV found.
type Parsed struct {
	Rows     []store.LifespanRow // every readable row; the register decides which changed
	Problems []Problem
}

var (
	// ErrNotLifespanCSV means the file has no header with the columns the update needs.
	ErrNotLifespanCSV = errors.New("not a filo lifespan CSV: the header needs id, export and keep_until columns")
	// ErrOldReviewCSV means the file is a review CSV from before keep_until.
	ErrOldReviewCSV = errors.New("this is a review CSV from an older filo; export again with: filo export lifespan")
)

// ReadCSV reads an edited lifespan CSV. It accepts what spreadsheets do to a file: a byte order mark, CRLF line ends,
// ';' or tab as the separator, columns moved or added, and empty rows. A row listed more than once is refused every
// time it appears, rather than guessing which one is meant.
func ReadCSV(r io.Reader) (Parsed, error) {
	var p Parsed
	data, err := io.ReadAll(r)
	if err != nil {
		return p, err
	}
	data = bytes.TrimPrefix(data, []byte(bom))

	var cr *csv.Reader
	var cols map[string]int
	var width int // header fields
	old := false
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
		if hasAll(m, "id", "export", "keep_until") {
			cr, cols, width = c, m, len(head)
			break
		}
		if hasAll(m, "id", "export", "review_date") {
			old = true
		}
	}
	if cr == nil {
		if old {
			return p, ErrOldReviewCSV
		}
		return p, ErrNotLifespanCSV
	}

	seen := map[[2]int64][]int{} // (export, id) -> indexes into p.Rows
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return p, err // a broken quote: csv says where
		}
		line, _ := cr.FieldPos(0)
		get := func(name string) string {
			i, ok := cols[name]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		bad := func(format string, args ...any) {
			p.Problems = append(p.Problems, Problem{line, fmt.Sprintf(format, args...)})
		}

		id, idErr := strconv.ParseInt(get("id"), 10, 64)
		ex, exErr := strconv.ParseInt(get("export"), 10, 64)
		switch {
		case idErr != nil || id < 1:
			bad("id %q is not a file ID", get("id"))
			continue
		case exErr != nil || ex < 1:
			bad("export %q is not an export number", get("export"))
			continue
		}
		// filo reads only keep_until, so a value typed past the last column would otherwise be lost silently.
		if v := beyond(rec, width); v != "" {
			bad("%q is outside the columns; put the date under keep_until", v)
			continue
		}
		date, err := ParseDate(get("keep_until"))
		if err != nil {
			bad("keep_until %v", err)
			continue
		}
		key := [2]int64{ex, id}
		seen[key] = append(seen[key], len(p.Rows))
		p.Rows = append(p.Rows, store.LifespanRow{Line: line, ExportID: ex, FileID: id, KeepUntil: date})
	}

	// Drop every copy of a row that appears more than once.
	drop := map[int]bool{}
	for _, idx := range seen {
		if len(idx) > 1 {
			for _, i := range idx {
				drop[i] = true
				r := p.Rows[i]
				p.Problems = append(p.Problems, Problem{r.Line,
					fmt.Sprintf("file %d of export %d is listed more than once", r.FileID, r.ExportID)})
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
