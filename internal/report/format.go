package report

import (
	"fmt"
	"strconv"
	"time"

	"github.com/alansav/filo/internal/view"
)

// Size formats a byte count with binary units: 512 B, 1.5 KiB, 118 GiB.
func Size(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	suffix := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}[exp]
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, suffix)
	}
	return fmt.Sprintf("%.1f %s", v, suffix)
}

// Count formats an integer with thousands separators: 52,310. (One implementation, in view.)
func Count(n int64) string { return view.Count(n) }

// Plural formats a count with a singular or plural noun: "1 group", "3 groups".
func Plural(n int64, one, many string) string { return view.Plural(n, one, many) }

// Time shows a stored UTC timestamp as local time to the minute; anything unparsable is shown as stored.
func Time(utc string) string {
	t, err := time.Parse(time.RFC3339Nano, utc)
	if err != nil {
		return utc
	}
	return LocalTime(t)
}

// LocalTime shows a time as local time to the minute.
func LocalTime(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }
