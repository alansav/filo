// Package review reads and writes the review CSV and resolves the dates typed into it. The register side (the queue,
// exports, applying decisions) is in package store.
package review

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
)

// Header is the CSV's columns, in order. The import finds columns by name, so a spreadsheet may reorder them or add
// its own; it reads only id, export, review_date, action and retain_until.
var Header = []string{"id", "export", "reason", "path", "size", "modified", "review_date", "action", "retain_until"}

const dateLayout = "2006-01-02"

// bom marks the file as UTF-8 for spreadsheets that would otherwise guess another encoding.
const bom = "\ufeff"

// ParseDate resolves a typed date: "" stays "", YYYY-MM-DD is checked, and +N followed by d, w, m or y (days, weeks,
// months, years) is added to today. A month step that lands past the end of a month stops at its last day. Anything
// else is refused: a date like 06/07/2027 is ambiguous and is never guessed.
func ParseDate(s string, today time.Time) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(s, "+") {
		if len(s) < 3 {
			return "", fmt.Errorf("%q: use YYYY-MM-DD, or +N followed by d, w, m or y (for example +30d or +7y)", s)
		}
		n, err := strconv.Atoi(s[1 : len(s)-1])
		if err != nil || n < 1 || n > 1000 {
			return "", fmt.Errorf("%q: use YYYY-MM-DD, or +N followed by d, w, m or y (for example +30d or +7y)", s)
		}
		y, m, d := today.Date()
		var t time.Time
		switch s[len(s)-1] {
		case 'd', 'D':
			t = time.Date(y, m, d+n, 0, 0, 0, 0, time.UTC)
		case 'w', 'W':
			t = time.Date(y, m, d+7*n, 0, 0, 0, 0, time.UTC)
		case 'm', 'M':
			t = addMonths(y, m, d, n)
		case 'y', 'Y':
			t = addMonths(y, m, d, 12*n)
		default:
			return "", fmt.Errorf("%q: use YYYY-MM-DD, or +N followed by d, w, m or y (for example +30d or +7y)", s)
		}
		return t.Format(dateLayout), nil
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return "", fmt.Errorf("%q is not a date: use YYYY-MM-DD (for example 2027-06-30), or +N followed by d, w, m or y", s)
	}
	return t.Format(dateLayout), nil
}

// addMonths adds n months to y-m-d, keeping the day but no later than the target month's last day.
func addMonths(y int, m time.Month, d, n int) time.Time {
	first := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(d, last), 0, 0, 0, 0, time.UTC)
}

// Today is t's date in its location, as YYYY-MM-DD.
func Today(t time.Time) string { return t.Format(dateLayout) }

// ReasonText says in words why a file is queued.
func ReasonText(it store.QueueItem) string {
	switch it.Reason {
	case store.ReasonUnreadable:
		return fmt.Sprintf("unreadable since scan %d (%s)", it.UnreadableSince, report.Time(it.UnreadableSinceUTC))
	case store.ReasonChanged:
		return fmt.Sprintf("changed since review (%s)", report.Time(it.ReviewedUTC))
	case store.ReasonPossibleMove:
		s := "possible move of " + it.MoveFrom
		if it.MoveDetail != "" {
			s += " (" + it.MoveDetail + ")"
		}
		return s
	case store.ReasonDue:
		return fmt.Sprintf("review due (%s)", it.ReviewDate)
	case store.ReasonUndated:
		return "no review date"
	}
	return string(it.Reason)
}

// cell makes a value safe to put in a spreadsheet: valid UTF-8, and never read as a formula. Only display columns
// pass through here; the import never reads them.
func cell(s string) string {
	s = strings.ToValidUTF8(s, "\ufffd")
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// WriteCSV writes the queue as one export. Times are shown in loc.
func WriteCSV(w io.Writer, exportID int64, items []store.QueueItem, loc *time.Location) error {
	if _, err := io.WriteString(w, bom); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(Header); err != nil {
		return err
	}
	ex := strconv.FormatInt(exportID, 10)
	for _, it := range items {
		rec := []string{
			strconv.FormatInt(it.FileID, 10), ex, cell(ReasonText(it)), cell(it.AbsPath()),
			strconv.FormatInt(it.Size, 10), time.Unix(0, it.MtimeNS).In(loc).Format("2006-01-02 15:04"),
			"", "", it.RetainUntil,
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// Problem is a row the import refuses before it reaches the register.
type Problem struct {
	Line int
	Msg  string
}

// Parsed is what ReadCSV found.
type Parsed struct {
	// Decisions are the rows with something filled in. A row with only retain_until is passed on too: the export
	// fills that column in, so the register decides whether it was changed.
	Decisions []store.Decision
	Blank     int // rows with nothing filled in: not reviewed, left alone
	Problems  []Problem
}

// ErrNotReviewCSV means the file has no header with the columns the import needs.
var ErrNotReviewCSV = errors.New("not a filo review CSV: the header needs id, export, review_date and action columns")

// ReadCSV reads an edited review CSV. It accepts what spreadsheets do to a file: a byte order mark, CRLF line ends,
// ';' or tab as the separator, columns moved or added, and empty rows. Dates are resolved against today. A row
// listed more than once is refused every time it appears, rather than guessing which one is meant.
func ReadCSV(r io.Reader, today time.Time) (Parsed, error) {
	var p Parsed
	data, err := io.ReadAll(r)
	if err != nil {
		return p, err
	}
	data = bytes.TrimPrefix(data, []byte(bom))

	var cr *csv.Reader
	var cols map[string]int
	var width int // header fields
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
		if hasAll(m, "id", "export", "review_date", "action") {
			cr, cols, width = c, m, len(head)
			break
		}
	}
	if cr == nil {
		return p, ErrNotReviewCSV
	}

	todayStr := Today(today)
	seen := map[[2]int64][]int{} // (export, id) -> indexes into p.Decisions
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

		rawDate, rawAction, rawRetain := get("review_date"), strings.ToLower(get("action")), get("retain_until")
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
		if rawDate == "" && rawAction == "" && rawRetain == "" {
			// Nothing to read, but a value typed in the wrong place must not pass as an untouched row.
			if v := beyond(rec, width); v != "" {
				bad("%q is outside the columns; put it under review_date, action or retain_until", v)
				continue
			}
			if v := get("reason"); v != "" && !exportedReason(v) {
				bad("the reason column was changed to %q, and filo does not read it; put it under review_date, "+
					"action or retain_until", v)
				continue
			}
			p.Blank++
			continue
		}

		d := store.Decision{Line: line, ExportID: ex, FileID: id}
		if rawAction != "" && rawAction != store.ActionDelete {
			bad("action %q: the only action is delete", get("action"))
			continue
		}
		d.Action = rawAction
		if d.ReviewDate, err = ParseDate(rawDate, today); err != nil {
			bad("review_date %v", err)
			continue
		}
		if _, ok := cols["retain_until"]; ok {
			if d.RetainUntil, err = ParseDate(rawRetain, today); err != nil {
				bad("retain_until %v", err)
				continue
			}
		}
		switch {
		case d.ReviewDate != "" && d.Action != "":
			bad("give a review date or delete, not both")
			continue
		case d.ReviewDate != "" && d.ReviewDate <= todayStr:
			bad("review_date %s is not after today", d.ReviewDate)
			continue
		}
		key := [2]int64{ex, id}
		seen[key] = append(seen[key], len(p.Decisions))
		p.Decisions = append(p.Decisions, d)
	}

	// Drop every copy of a row that appears more than once.
	drop := map[int]bool{}
	for _, idx := range seen {
		if len(idx) > 1 {
			for _, i := range idx {
				drop[i] = true
				d := p.Decisions[i]
				p.Problems = append(p.Problems, Problem{d.Line,
					fmt.Sprintf("file %d of export %d is listed more than once", d.FileID, d.ExportID)})
			}
		}
	}
	if len(drop) > 0 {
		kept := p.Decisions[:0]
		for i, d := range p.Decisions {
			if !drop[i] {
				kept = append(kept, d)
			}
		}
		p.Decisions = kept
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

// exportedReason reports whether a reason cell still reads as one ReasonText wrote.
func exportedReason(v string) bool {
	for _, prefix := range []string{"no review date", "review due (", "unreadable since scan ", "changed since review",
		"possible move of "} {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}

func hasAll(m map[string]int, names ...string) bool {
	for _, n := range names {
		if _, ok := m[n]; !ok {
			return false
		}
	}
	return true
}
