package view

import "strconv"

// Problem is something refused or skipped: where it is (a CSV line, a group, a file) and why, with what to do.
type Problem struct {
	Line      int    // the CSV line, 0 = none
	Group     int    // the CSV group, 0 = none
	Root, Rel string // the file, if any
	Reason    string
}

// collapseAt is how many problems from CSV lines with one reason are listed one by one; more collapse into one line.
const collapseAt = 10

// Problems writes a list of problems under its title, each under the marker m (Refused for what was refused,
// Attention for what was skipped). Every problem is listed, since each is something to fix, but more than
// collapseAt problems from CSV lines with the same reason collapse into one line with their count and line range (a
// sorted CSV can refuse every row for one reason). Problems without a line (a file skipped) are always listed one by
// one: a run's output is its record.
func (r *Renderer) Problems(title string, m Mark, ps []Problem) {
	if len(ps) == 0 {
		return
	}
	r.write(styled(title, bold))
	byReason := map[string]int{} // problems from CSV lines, by reason
	for _, p := range ps {
		if p.Line > 0 {
			byReason[p.Reason]++
		}
	}
	hasLine, hasGroup := false, false
	for _, p := range ps {
		hasLine = hasLine || p.Line > 0
		hasGroup = hasGroup || p.Group > 0
	}
	done := map[string]bool{}
	for _, p := range ps {
		if n := byReason[p.Reason]; p.Line > 0 && n > collapseAt {
			if done[p.Reason] {
				continue
			}
			done[p.Reason] = true
			lo, hi := p.Line, p.Line
			for _, q := range ps {
				if q.Reason == p.Reason && q.Line > 0 {
					lo, hi = min(lo, q.Line), max(hi, q.Line)
				}
			}
			where := "lines " + strconv.Itoa(lo) + "-" + strconv.Itoa(hi) + "   " + strconv.Itoa(n) + " rows"
			r.write(spaces(2), r.mark(m), plain(" "+where+"   "+p.Reason))
			continue
		}
		spans := []span{spaces(2), r.mark(m), plain(" ")}
		if hasGroup {
			g := ""
			if p.Group > 0 {
				g = "group " + strconv.Itoa(p.Group)
			}
			spans = append(spans, plain(g), spaces(10-len(g)))
		}
		if hasLine {
			l := ""
			if p.Line > 0 {
				l = "line " + strconv.Itoa(p.Line)
			}
			spans = append(spans, plain(l), spaces(10-len(l)))
		}
		if p.Root != "" || p.Rel != "" {
			spans = append(spans, name(r.Path(p.Root, p.Rel)), plain(": "))
		}
		spans = append(spans, plain(p.Reason))
		r.write(spans...)
	}
}
