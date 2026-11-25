// Package sheet writes and reads the CSV files filo exports for editing in a spreadsheet, accepting what spreadsheets
// do to them: a byte order mark, CRLF line ends, ';' or tab as the separator, columns moved or added, and empty
// rows. Packages retention, tagcsv and duplicates hold what their columns mean.
package sheet

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
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

// NewWriter starts a CSV for a spreadsheet on w: the byte order mark, then the header.
func NewWriter(w io.Writer, header []string) (*csv.Writer, error) {
	if _, err := io.WriteString(w, BOM); err != nil {
		return nil, err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return nil, err
	}
	return cw, nil
}

// FileCells are the cells every export shows of a file at rel ('/'-separated) in the root at root: its directory
// (the root's path and the file's directory in it), name, size and modified time (in loc, to the minute).
func FileCells(root, rel string, size, mtimeNS int64, loc *time.Location) []string {
	dir, name := path.Split(rel)
	directory := root
	if dir = strings.TrimSuffix(dir, "/"); dir != "" {
		directory = root + "/" + dir
	}
	return []string{Cell(directory), Cell(name), strconv.FormatInt(size, 10),
		time.Unix(0, mtimeNS).In(loc).Format("2006-01-02 15:04")}
}

// Problem is a row an update refuses before it reaches the register.
type Problem struct {
	Line int
	Msg  string
}

// ReadIDs reads a row's file ID and export number, and checks that nothing was typed past the header's columns.
// When it cannot, it returns why, with the column to type into named by under ("file_date or retention").
func ReadIDs(row Row, under string) (id, export int64, problem string) {
	id, idErr := strconv.ParseInt(row.Get("id"), 10, 64)
	export, exErr := strconv.ParseInt(row.Get("export"), 10, 64)
	switch {
	case idErr != nil || id < 1:
		return 0, 0, fmt.Sprintf("id %q is not a file ID", row.Get("id"))
	case exErr != nil || export < 1:
		return 0, 0, fmt.Sprintf("export %q is not an export number", row.Get("export"))
	case row.Beyond() != "":
		return 0, 0, fmt.Sprintf("%q is outside the columns; put it under %s", row.Beyond(), under)
	}
	return id, export, ""
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
// that there are no more than MaxRows, without parsing them: it follows the quotes as csv does, so a separator or
// line end inside a quoted field is part of it. A quote csv refuses (a bare quote in an unquoted field, or text after
// a closing quote) ends the check there: csv stops with its own error at that row, so nothing after it is parsed.
// The error says on which line the row starts.
func checkLimits(data []byte, sep byte, headerOnly bool) error {
	const (
		fieldStart = iota
		unquoted
		quoted
		afterQuote // a quote inside a quoted field: it closes it, or escapes a second quote
	)
	state := fieldStart
	rows, fields, start, line, startLine := 0, 1, 0, 1, 1
	for i, b := range data {
		if fields > MaxFields {
			return fmt.Errorf("line %d has more than %d fields, more than filo reads", startLine, MaxFields)
		}
		if i-start >= MaxRecord {
			return fmt.Errorf("the row on line %d is over %d MB, more than filo reads", startLine, MaxRecord>>20)
		}
		if b == '\n' {
			line++
		}
		switch state {
		case quoted:
			if b == '"' {
				state = afterQuote
			}
			continue
		case afterQuote:
			switch b {
			case '"':
				state = quoted
				continue
			case sep, '\n', '\r':
			default:
				return nil // text after a closing quote: csv refuses the row
			}
		case unquoted:
			if b == '"' {
				return nil // a bare quote: csv refuses the row
			}
		case fieldStart:
			if b == '"' {
				state = quoted
				continue
			}
		}
		switch b {
		case sep:
			fields++
			state = fieldStart
		case '\n':
			rows++
			if headerOnly {
				return nil
			}
			if rows == MaxRows && i+1 < len(data) {
				return fmt.Errorf("the CSV has more than %d rows, more than filo reads; export a smaller directory",
					MaxRows)
			}
			fields, start, startLine, state = 1, i+1, line, fieldStart
		case '\r':
			// part of a CRLF line end; csv drops it
		default:
			state = unquoted
		}
	}
	if fields > MaxFields {
		return fmt.Errorf("line %d has more than %d fields, more than filo reads", startLine, MaxFields)
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
