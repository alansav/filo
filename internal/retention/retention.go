// Package retention reads the retention cells of the combined CSV (file_date, retention and keep_until, the date a
// file is kept until), works out keep_until from a retention, and reads dates from file names. The register side
// (listing files, recording exports, applying updates) is in package store; the CSV itself is package filecsv's.
package retention

import (
	"fmt"
	"strings"
	"time"

	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/store"
)

// Header is the combined CSV's columns up to its retention cells, in order (package filecsv adds tags). The update
// finds columns by name, so a spreadsheet may reorder them or add its own; of these it reads id, export, file_date
// and retention, and checks that keep_until (worked out by filo, for display) was left alone.
var Header = []string{"id", "export", "directory", "name", "size", "modified", "file_date", "retention", "keep_until"}

const dateLayout = store.DateLayout

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

// parseFileDate checks a file_date cell: a date as ParseDate takes it, or one marked as read from the file name
// ("!2023-07-14", store.MarkedDate), which is passed on marked for the update to decide.
func parseFileDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !store.IsMarkedDate(s) {
		return ParseDate(s)
	}
	d, err := ParseDate(strings.TrimPrefix(s, "!"))
	if err != nil || d == "" {
		return "", fmt.Errorf("%q is not a date: remove the ! and use YYYY-MM-DD, for example 2023-03-14%s", s, formatHint)
	}
	return store.MarkedDate(d), nil
}

// ParseRetention checks a typed retention: "" stays "" (a blank cell); forever (in any case) is store.Forever; a
// period is N followed by y, m, w or d (years, months, weeks, days; N from 1 to 1000), returned in lower case; a
// date is YYYY-MM-DD, a real date; and a policy name (store.PolicyName) is returned in lower case. Anything else is
// refused.
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
	if d, err := ParseDate(s); err == nil {
		return d, nil
	}
	if name, err := store.PolicyName(s); err == nil {
		return name, nil // a policy (docs/design.md, Named retention policies)
	}
	return "", fmt.Errorf("%q is not a retention: use a period such as 7y, 18m, 6w or 30d (counted from "+
		"file_date), forever, a date YYYY-MM-DD, or a policy name (letters, digits, - and _)%s", s, formatHint)
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

// ReadRow reads a row's retention cells, the file_date and keep_until ones if the sheet has them, for the file id of
// export ex: the row, or why it is refused.
func ReadRow(sh *sheet.Sheet, row sheet.Row, id, ex int64) (store.RetentionRow, string) {
	rr := store.RetentionRow{Line: row.Line, ExportID: ex, FileID: id}
	var err error
	if rr.Retention, err = ParseRetention(row.Get("retention")); err != nil {
		return rr, fmt.Sprintf("retention %v", err)
	}
	if sh.Has("file_date") {
		rr.HasFileDate = true
		if rr.FileDate, err = parseFileDate(row.Get("file_date")); err != nil {
			return rr, fmt.Sprintf("file_date %v", err)
		}
	}
	if sh.Has("keep_until") {
		rr.HasKeepUntil = true
		if rr.KeepUntil, err = parseKeepUntil(row.Get("keep_until")); err != nil {
			return rr, fmt.Sprintf("keep_until %v", err)
		}
	}
	return rr, ""
}
