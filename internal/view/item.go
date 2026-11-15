package view

import (
	"strconv"
	"strings"
)

// Item writes the head of a select-mode item: its place in the run and what it is, "[3/14]  past its retention ·
// until 2024-12-31 · 12 MB". The lines under it (ItemPath) show full paths: it is the last look before a deletion.
func (r *Renderer) Item(n, total int, facts ...string) {
	r.write(styled("["+strconv.Itoa(n)+"/"+strconv.Itoa(total)+"]", bold), plain("  "+strings.Join(facts, r.sep())))
}

// ItemPath writes a file under an item, in full, with what happens to it ("keep", "delete", or "" for a file alone)
// and a note.
func (r *Renderer) ItemPath(word, root, rel, note string) {
	spans := []span{spaces(10)}
	switch word {
	case "keep":
		spans = append(spans, styled("keep", green), spaces(5))
	case "delete":
		spans = append(spans, styled("delete", red), spaces(3))
	case "":
	default:
		spans = append(spans, plain(word), spaces(9-len(word)))
	}
	spans = append(spans, name(r.Full(root, rel)))
	if note != "" {
		spans = append(spans, spaces(3), styled(note, dim))
	}
	r.write(spans...)
}

// ResultFile writes what was done with a file under an item: "        ✓ deleted b/contract.pdf", or with a reason
// after the file. The file is shown as a name (a trailing space as \x20).
func (r *Renderer) ResultFile(m Mark, what, root, rel, reason string) {
	spans := []span{spaces(8), r.mark(m), plain(" " + what + " "), name(r.Path(root, rel))}
	if reason != "" {
		spans = append(spans, plain(": "+r.shorten(reason)))
	}
	r.write(spans...)
}

// Result writes what was done with an item, under its choices: "        ✓ deleted".
func (r *Renderer) Result(m Mark, text string) { r.write(spaces(8), r.mark(m), plain(" "+text)) }
