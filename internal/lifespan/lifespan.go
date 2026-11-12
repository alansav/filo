// Package lifespan reads and writes the lifespan CSV: every present file with its file_date, retention and the date it
// is kept until, works out keep_until from a retention, and reads dates from file names. The register
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
// its own; it reads only id, export, file_date and retention, and checks that keep_until (worked out by filo, for
// display) was left alone.
var Header = []string{"id", "export", "directory", "name", "size", "modified", "file_date", "retention", "keep_until"}

const dateLayout = store.DateLayout

// bom marks the file as UTF-8 for spreadsheets that would otherwise guess another encoding.
const bom = "\ufeff"

const formatHint = " (a spreadsheet may have rewritten it; format the column as text)"

// ParseDate checks a typed date: "" stays "" (a blank cell), and YYYY-MM-DD must be a real date (2027-02-29 is not).
// Anything else is refused: a date like 06/07/2027 is ambiguous and is never guessed.
func ParseDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return "", fmt.Errorf("%q is not a date: use YYYY-MM-DD, for example 2023-03-14%s", s, formatHint)
	}
	return t.Format(dateLayout), nil
}

// ParseRetention checks a typed retention: "" stays "" (a blank cell); forever (in any case) is store.Forever; a
// period is N followed by y, m, w or d (years, months, weeks, days; N from 1 to 1000), returned in lower case; and a
// date is YYYY-MM-DD, a real date. Anything else is refused.
func ParseRetention(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.EqualFold(s, store.Forever) {
		return store.Forever, nil
	}
	if p, ok := store.NormalPeriod(s); ok {
		return p, nil
	}
	d, err := ParseDate(s)
	if err != nil {
		return "", fmt.Errorf("%q is not a retention: use a period such as 7y, 18m, 6w or 30d (counted from "+
			"file_date), forever, or a date YYYY-MM-DD%s", s, formatHint)
	}
	return d, nil
}

// parseKeepUntil reads the display-only keep_until cell, so that a cell a spreadsheet merely reformatted compares
// equal to what was exported: "" , forever (any case) or a date. A value that is none of these is refused with the
// same hint as the other date columns.
func parseKeepUntil(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, store.Forever) {
		return store.Forever, nil
	}
	return ParseDate(s)
}

// DateReader reads file_dates from file names against one fixed day, so a long export uses the same bounds for every
// file even if it runs past midnight.
type DateReader struct{ latest string }

// NewDateReader makes a DateReader for today (YYYY-MM-DD). With an invalid today it reads no dates.
func NewDateReader(today string) DateReader {
	t, err := time.Parse(dateLayout, today)
	if err != nil {
		return DateReader{}
	}
	return DateReader{latest: t.AddDate(0, 0, 30).Format(dateLayout)}
}

// DateFromName reads a file_date from a file name against today (YYYY-MM-DD); see DateReader.Read.
func DateFromName(name, today string) string { return NewDateReader(today).Read(name) }

// Read reads a file_date from a file name, or returns "". Accepted, with a non-digit (or the start or end of the name)
// on both sides: YYYY-MM-DD (also with _ or . as the separator), YYYYMMDD, and YYYY-MM (also _ or .), which gives the
// last day of that month so a file is never kept for less time than meant. The date must be real and between
// 1900-01-01 and 30 days after the reader's today. A name with two different dates gives "": never guessed.
func (r DateReader) Read(name string) string {
	latest := r.latest
	if latest == "" {
		return ""
	}
	found := ""
	for i := 0; i < len(name); i++ {
		if !isDigit(name[i]) || (i > 0 && isDigit(name[i-1])) {
			continue // a date starts a run of digits
		}
		d, ok := dateAt(name, i)
		if !ok || d < "1900-01-01" || d > latest {
			continue
		}
		if found != "" && found != d {
			return ""
		}
		found = d
	}
	return found
}

// dateAt reads a date starting at s[i], which starts a run of digits.
func dateAt(s string, i int) (string, bool) {
	digits := func(at, n int) (string, bool) {
		if at+n > len(s) {
			return "", false
		}
		for k := at; k < at+n; k++ {
			if !isDigit(s[k]) {
				return "", false
			}
		}
		return s[at : at+n], true
	}
	endsAt := func(at int) bool { return at == len(s) || !isDigit(s[at]) }
	isSep := func(at int) bool { return at < len(s) && strings.IndexByte("-_.", s[at]) >= 0 }
	realDate := func(y, m, d string) (string, bool) {
		t, err := time.Parse(dateLayout, y+"-"+m+"-"+d)
		return t.Format(dateLayout), err == nil
	}

	y, ok := digits(i, 4)
	if !ok {
		return "", false
	}
	if isSep(i + 4) {
		m, ok := digits(i+5, 2)
		if !ok {
			return "", false
		}
		if isSep(i+7) && s[i+7] == s[i+4] {
			if d, ok := digits(i+8, 2); ok && endsAt(i+10) {
				return realDate(y, m, d)
			}
		}
		// Anything more after the month (more digits, or a separator and digits that are not a day) makes this
		// something other than a month: not read.
		if !endsAt(i+7) || (isSep(i+7) && i+8 < len(s) && isDigit(s[i+8])) {
			return "", false
		}
		// YYYY-MM: the last day of the month.
		t, err := time.Parse("2006-01", y+"-"+m)
		if err != nil {
			return "", false
		}
		return t.AddDate(0, 1, -1).Format(dateLayout), true
	}
	if all, ok := digits(i, 8); ok && endsAt(i+8) {
		return realDate(all[:4], all[4:6], all[6:])
	}
	return "", false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

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

// WriteCSV writes items as one export: the file_date, retention and keep_until they carry (the caller fills in
// file_date from the name first, and records the same values with the export). Times are shown in loc.
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
			time.Unix(0, it.MtimeNS).In(loc).Format("2006-01-02 15:04"), it.FileDate, it.Retention, it.KeepUntil,
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
	ErrNotLifespanCSV = errors.New("not a filo lifespan CSV: the header needs id, export and retention columns")
	// ErrOldCSV means the file is a review or lifespan CSV from an older filo.
	ErrOldCSV = errors.New("this CSV is from an older filo; export again with: filo export lifespan")
	// ErrNoRetention means a current lifespan CSV lost its retention column (deleted or renamed in the spreadsheet).
	ErrNoRetention = errors.New("the CSV has no retention column, which filo update needs; put it back with that " +
		"name, or export again")
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
	old, missing := false, false
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
		if hasAll(m, "id", "export", "retention") {
			cr, cols, width = c, m, len(head)
			break
		}
		switch {
		case hasAll(m, "id", "export", "file_date"):
			missing = true // a current CSV without its retention column
		case hasAll(m, "id", "export", "review_date") || hasAll(m, "id", "export", "keep_until"):
			old = true
		}
	}
	if cr == nil {
		switch {
		case missing:
			return p, ErrNoRetention
		case old:
			return p, ErrOldCSV
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
		// A value typed past the last column would otherwise be lost silently.
		if v := beyond(rec, width); v != "" {
			bad("%q is outside the columns; put it under file_date or retention", v)
			continue
		}
		row := store.LifespanRow{Line: line, ExportID: ex, FileID: id}
		if row.Retention, err = ParseRetention(get("retention")); err != nil {
			bad("retention %v", err)
			continue
		}
		if _, ok := cols["file_date"]; ok {
			row.HasFileDate = true
			if row.FileDate, err = ParseDate(get("file_date")); err != nil {
				bad("file_date %v", err)
				continue
			}
		}
		if _, ok := cols["keep_until"]; ok {
			row.HasKeepUntil = true
			if row.KeepUntil, err = parseKeepUntil(get("keep_until")); err != nil {
				bad("keep_until %v", err)
				continue
			}
		}
		key := [2]int64{ex, id}
		seen[key] = append(seen[key], len(p.Rows))
		p.Rows = append(p.Rows, row)
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
