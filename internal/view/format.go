package view

import "strconv"

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
