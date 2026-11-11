package report

import (
	"fmt"
	"strconv"
	"time"
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

// Count formats an integer with thousands separators: 52,310.
func Count(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := s[0] == '-'
	if neg {
		s = s[1:]
	}
	var out []byte
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// Plural formats a count with a singular or plural noun: "1 group", "3 groups".
func Plural(n int64, one, many string) string {
	if n == 1 {
		return Count(n) + " " + one
	}
	return Count(n) + " " + many
}

// Time shows a stored UTC timestamp as local time to the minute; anything unparsable is shown as stored.
func Time(utc string) string {
	t, err := time.Parse(time.RFC3339Nano, utc)
	if err != nil {
		return utc
	}
	return t.Local().Format("2006-01-02 15:04")
}
