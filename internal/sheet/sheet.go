// Package sheet writes and reads the CSV files filo exports for editing in a spreadsheet, accepting what spreadsheets
// do to them: a byte order mark, CRLF line ends, ';' or tab as the separator, columns moved or added, and empty
// rows. Packages retention and duplicates hold what their columns mean.
package sheet

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
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

// Limits on an edited CSV, so a huge or malformed file is refused before it is parsed, never read until memory runs
// out. A spreadsheet keeps at most MaxRows rows and MaxFields columns, so a CSV it saved stays within them.
const (
	MaxSize   = 256 << 20 // bytes in the file
	MaxRows   = 1 << 20   // rows, the header and empty rows included (1,048,576)
	MaxFields = 1 << 14   // fields in one row (16,384)
	MaxRecord = 1 << 20   // bytes in one row
)

// ErrTooLarge means a CSV is over MaxSize.
var ErrTooLarge = fmt.Errorf("the CSV is over %d MB, more than filo reads; export a smaller directory", MaxSize>>20)

// ReadAll reads a CSV from r, refusing it with ErrTooLarge once it is over MaxSize: no more than MaxSize+1 bytes are
// ever read.
func ReadAll(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSize {
		return nil, ErrTooLarge
	}
	return data, nil
}

// ReadFile reads the CSV at path once, within MaxSize (ReadAll).
func ReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() && fi.Size() > MaxSize {
		return nil, ErrTooLarge
	}
	return ReadAll(f)
}

// Sheet is an edited CSV whose header has been read.
type Sheet struct {
	r     *csv.Reader
	data  []byte // the whole CSV, without its byte order mark
	sep   byte
	cols  map[string]int // column name, lower case -> index
	width int            // header fields
}

// Open finds the separator (',', ';' or tab) whose header has every column in want, by name in any case. Without
// one it returns nil and the headers it could read, so the caller can say what the file looks like instead. A file
// over MaxSize, or a header over the row limits, is not read at all.
func Open(data []byte, want ...string) (*Sheet, []map[string]int) {
	if len(data) > MaxSize {
		return nil, nil
	}
	data = bytes.TrimPrefix(data, []byte(BOM))
	var headers []map[string]int
	for _, sep := range []byte{',', ';', '\t'} {
		if err := checkLimits(data, sep, true); err != nil {
			continue
		}
		c := csv.NewReader(bytes.NewReader(data))
		c.Comma = rune(sep)
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
			return &Sheet{r: c, data: data, sep: sep, cols: cols, width: len(head)}, nil
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

// Each calls fn with every non-empty row, in order. A broken quote stops it with an error saying where; so does a
// CSV over the limits (MaxRows, MaxFields, MaxRecord), before any row is read.
func (s *Sheet) Each(fn func(Row)) error {
	if err := checkLimits(s.data, s.sep, false); err != nil {
		return err
	}
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

// checkLimits checks data's rows (only the first, the header, if headerOnly) against MaxFields and MaxRecord, and
// that there are no more than MaxRows, without parsing them: it only follows the quotes, as csv does, so a separator
// or line end inside quotes is part of a field. The error says on which line the row starts.
func checkLimits(data []byte, sep byte, headerOnly bool) error {
	rows, fields, start, line, startLine := 0, 1, 0, 1, 1
	quoted := false
	for i, b := range data {
		switch {
		case b == '"':
			quoted = !quoted
		case b == sep && !quoted:
			fields++
		}
		if fields > MaxFields {
			return fmt.Errorf("line %d has more than %d fields, more than filo reads", startLine, MaxFields)
		}
		if i-start >= MaxRecord {
			return fmt.Errorf("the row on line %d is over %d MB, more than filo reads", startLine, MaxRecord>>20)
		}
		if b != '\n' {
			continue
		}
		line++
		if quoted {
			continue
		}
		rows++
		if headerOnly {
			return nil
		}
		if rows == MaxRows && i+1 < len(data) {
			return fmt.Errorf("the CSV has more than %d rows, more than filo reads; export a smaller directory",
				MaxRows)
		}
		fields, start, startLine = 1, i+1, line
	}
	return nil
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
