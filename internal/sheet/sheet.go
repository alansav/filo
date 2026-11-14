// Package sheet writes and reads the CSV files filo exports for editing in a spreadsheet, accepting what spreadsheets
// do to them: a byte order mark, CRLF line ends, ';' or tab as the separator, columns moved or added, and empty
// rows. Packages retention and duplicates hold what their columns mean.
package sheet

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strings"
)

// BOM marks the file as UTF-8 for spreadsheets that would otherwise guess another encoding.
const BOM = "\ufeff"

// Cell makes a display value safe to put in a spreadsheet: valid UTF-8, and never read as a formula. Only display
// columns pass through here; updates never read them.
func Cell(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// Sheet is an edited CSV whose header has been read.
type Sheet struct {
	r     *csv.Reader
	cols  map[string]int // column name, lower case -> index
	width int            // header fields
}

// Open finds the separator (',', ';' or tab) whose header has every column in want, by name in any case. Without
// one it returns nil and the headers it could read, so the caller can say what the file looks like instead.
func Open(data []byte, want ...string) (*Sheet, []map[string]int) {
	data = bytes.TrimPrefix(data, []byte(BOM))
	var headers []map[string]int
	for _, sep := range []rune{',', ';', '\t'} {
		c := csv.NewReader(bytes.NewReader(data))
		c.Comma = sep
		c.FieldsPerRecord = -1
		head, err := c.Read()
		if err != nil {
			continue
		}
		cols := map[string]int{}
		for i, h := range head {
			name := strings.ToLower(strings.TrimSpace(h))
			if _, dup := cols[name]; !dup {
				cols[name] = i
			}
		}
		if HasAll(cols, want...) {
			return &Sheet{r: c, cols: cols, width: len(head)}, nil
		}
		headers = append(headers, cols)
	}
	return nil, headers
}

// Has reports whether the sheet has a column.
func (s *Sheet) Has(name string) bool {
	_, ok := s.cols[name]
	return ok
}

// Row is one non-empty row.
type Row struct {
	Line int // in the file, for messages
	rec  []string
	s    *Sheet
}

// Get returns the row's value in a column, trimmed; "" if the sheet or the row has no such column.
func (r Row) Get(name string) string {
	i, ok := r.s.cols[name]
	if !ok || i >= len(r.rec) {
		return ""
	}
	return strings.TrimSpace(r.rec[i])
}

// Beyond returns the first value typed past the header's columns, or "": it would otherwise be lost silently.
func (r Row) Beyond() string {
	for i := r.s.width; i < len(r.rec); i++ {
		if v := strings.TrimSpace(r.rec[i]); v != "" {
			return v
		}
	}
	return ""
}

// Each calls fn with every non-empty row, in order. A broken quote stops it with an error saying where.
func (s *Sheet) Each(fn func(Row)) error {
	for {
		rec, err := s.r.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		line, _ := s.r.FieldPos(0)
		fn(Row{Line: line, rec: rec, s: s})
	}
}

// DropRepeated returns rows without any row whose key appears more than once, calling refused for each copy
// dropped: a row listed twice is never guessed at.
func DropRepeated[T any, K comparable](rows []T, key func(T) K, refused func(T)) []T {
	n := map[K]int{}
	for _, r := range rows {
		n[key(r)]++
	}
	var kept []T
	for _, r := range rows {
		if n[key(r)] > 1 {
			refused(r)
			continue
		}
		kept = append(kept, r)
	}
	return kept
}

// HasAll reports whether cols has every one of names.
func HasAll(cols map[string]int, names ...string) bool {
	for _, n := range names {
		if _, ok := cols[n]; !ok {
			return false
		}
	}
	return true
}
