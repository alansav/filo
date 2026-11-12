package store

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Forever is the retention (and keep_until) of a file kept indefinitely. It is stored as this word, never as a
// far-off date such as 9999-12-31, and checked explicitly rather than by comparing it with dates.
const Forever = "forever"

const dateLayout = "2006-01-02"

// period splits a retention period like 7y into its count and unit (y, m, w or d, in lower case). The count is 1 to
// 1000.
func period(s string) (int, byte, bool) {
	if len(s) < 2 {
		return 0, 0, false
	}
	unit := s[len(s)-1] | 0x20 // lower case
	if !strings.ContainsRune("ymwd", rune(unit)) {
		return 0, 0, false
	}
	digits := s[:len(s)-1]
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, 0, false
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 || n > 1000 {
		return 0, 0, false
	}
	return n, unit, true
}

// IsPeriod reports whether a retention is a period (7y, 18m, 6w, 30d), which counts from a file_date.
func IsPeriod(retention string) bool {
	_, _, ok := period(retention)
	return ok
}

// KeepUntil works out the date a file is kept until from its file_date (YYYY-MM-DD or "") and a retention: a period
// counts from fileDate (an error if there is none), forever stays forever, and a date is itself. "" gives "". A month
// step that lands past the end of a month stops on its last day.
func KeepUntil(fileDate, retention string) (string, error) {
	n, unit, ok := period(retention)
	if !ok {
		return retention, nil
	}
	if fileDate == "" {
		return "", fmt.Errorf("retention %s needs a file_date to count from", retention)
	}
	t, err := time.Parse(dateLayout, fileDate)
	if err != nil {
		return "", fmt.Errorf("file_date %q is not a date", fileDate)
	}
	y, m, d := t.Date()
	switch unit {
	case 'y':
		t = addMonths(y, m, d, 12*n)
	case 'm':
		t = addMonths(y, m, d, n)
	case 'w':
		t = t.AddDate(0, 0, 7*n)
	default:
		t = t.AddDate(0, 0, n)
	}
	return t.Format(dateLayout), nil
}

// addMonths adds n months to y-m-d, keeping the day but no later than the target month's last day.
func addMonths(y int, m time.Month, d, n int) time.Time {
	first := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	return time.Date(first.Year(), first.Month(), min(d, last), 0, 0, 0, 0, time.UTC)
}
