package view

import "unicode"

// columns is how many terminal columns s takes: two for a wide character (CJK, Hangul, fullwidth forms, most emoji),
// none for a combining mark or a zero-width character, one otherwise. It is a close approximation, from a small
// table of the main wide ranges of Unicode's East Asian Width property, not the full one: a rare emoji sequence may
// be counted a column or two off, which only shifts alignment. Nothing is ever cut because of it.
func columns(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff || (r >= 0xfe00 && r <= 0xfe0f) ||
			unicode.In(r, unicode.Mn, unicode.Me):
		case wide(r):
			n += 2
		default:
			n++
		}
	}
	return n
}

// wideRanges are the main ranges terminals show two columns wide.
var wideRanges = [][2]rune{
	{0x1100, 0x115f},   // Hangul Jamo initials
	{0x231a, 0x231b},   // watch, hourglass
	{0x2329, 0x232a},   // angle brackets
	{0x23e9, 0x23ec},   // media controls
	{0x2e80, 0x303e},   // CJK radicals, symbols and punctuation
	{0x3041, 0x33ff},   // Hiragana, Katakana, CJK compatibility
	{0x3400, 0x4dbf},   // CJK extension A
	{0x4e00, 0x9fff},   // CJK unified ideographs
	{0xa000, 0xa4cf},   // Yi
	{0xa960, 0xa97f},   // Hangul Jamo extended A
	{0xac00, 0xd7a3},   // Hangul syllables
	{0xf900, 0xfaff},   // CJK compatibility ideographs
	{0xfe10, 0xfe19},   // vertical forms
	{0xfe30, 0xfe6f},   // CJK compatibility forms, small forms
	{0xff00, 0xff60},   // fullwidth forms
	{0xffe0, 0xffe6},   // fullwidth signs
	{0x1f300, 0x1f64f}, // pictographs, emoticons
	{0x1f680, 0x1f6ff}, // transport and map symbols
	{0x1f900, 0x1f9ff}, // supplemental symbols and pictographs
	{0x1fa70, 0x1faff}, // symbols and pictographs extended A
	{0x20000, 0x3fffd}, // CJK extensions B and beyond
}

func wide(r rune) bool {
	for _, w := range wideRanges {
		if r >= w[0] && r <= w[1] {
			return true
		}
	}
	return false
}

// Fit escapes text (see Escape) and, if it takes cols columns or more, cuts it to one column short of cols (some
// consoles wrap as soon as the last column is written), ending in "…". Only the progress line uses it: it is
// redrawn in place, so a line that wraps cannot be cleared, and it is never acted on. Every other path is shown
// whole. cols 0 means no width: nothing is cut.
func Fit(text string, cols int) string {
	s := Escape(text)
	switch {
	case cols == 1:
		return "" // no room for anything that would not wrap
	case cols <= 0 || columns(s) < cols:
		return s
	}
	limit := cols - 2 // leaves room for "…" and the last column
	n := 0
	for i, r := range s {
		w := columns(string(r))
		if n+w > limit {
			return s[:i] + "…"
		}
		n += w
	}
	return s
}
