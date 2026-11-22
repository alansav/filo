package view

import "strconv"

// Subject is the title line of one thing shown in full (filo show): its name, then a summary.
type Subject struct {
	N, Total  int      // its place in the run: "[N/Total]" in front when Total > 1
	Name      string   // the file's name, shown as a name
	Status    string   // the summary's first fact: "kept until 2029-01-31"
	Attention bool     // the status needs a decision: the ! marker before it
	Facts     []string // the summary's other facts: "184 KiB"
	Dir       string   // the directory it is in, absolute, shown last with ~ for home; "" = none
}

// Subject writes a title line: "[1/3] 2022-01 Gas.pdf   kept until 2029-01-31 · 184 KiB · ~/Documents/bills". It
// starts a new thing: paths after it are shown in full, with ~ for home.
func (r *Renderer) Subject(s Subject) {
	r.root = ""
	var spans []span
	if s.Total > 1 {
		spans = append(spans, styled("["+strconv.Itoa(s.N)+"/"+strconv.Itoa(s.Total)+"]", bold), plain(" "))
	}
	spans = append(spans, styledName(s.Name, bold), plain("   "))
	if s.Attention {
		spans = append(spans, r.mark(Attention), plain(" "))
	}
	spans = append(spans, plain(s.Status))
	for _, f := range s.Facts {
		spans = append(spans, plain(r.sep()+f))
	}
	if s.Dir != "" {
		spans = append(spans, plain(r.sep()), name(r.Tilde(s.Dir)))
	}
	r.write(spans...)
}

// Part writes the title of a part of a thing shown in full, indented under its Subject: "  Copies   1 other file
// with the same content · counted as a duplicate".
func (r *Renderer) Part(title string, facts ...string) {
	spans := []span{spaces(2), styled(title, bold)}
	if len(facts) > 0 {
		spans = append(spans, plain("   "+r.Join(facts...)))
	}
	r.write(spans...)
}

// Field is one labelled line of a Part: a value, then a dimmed note, or a note that needs attention.
type Field struct {
	Label     string
	Value     string // plain text, or a path when Path is set
	Path      bool   // Value is an absolute path: shown as a name, with ~ for home
	Note      string // after the value, dimmed: "in 2 years 2 months"
	Attention string // after the value (or in its place), under the ! marker: "changed on disk since the last scan"
}

// LabelWidth is the width of the longest label in the field lists: what Fields lines values up after, so the parts
// of one thing line up with each other.
func LabelWidth(lists ...[]Field) int {
	w := 0
	for _, fields := range lists {
		for _, f := range fields {
			w = max(w, width(plain(f.Label)))
		}
	}
	return w
}

// Fields writes a Part's fields, labels indented by four and values lined up two columns after labelWidth
// (LabelWidth of every part shown together), or after the longest label here, if longer:
// "    modified    2022-02-03 09:14   unchanged since the last scan (2026-11-20)".
func (r *Renderer) Fields(labelWidth int, fields []Field) {
	w := max(labelWidth, LabelWidth(fields))
	for _, f := range fields {
		spans := []span{spaces(4), plain(f.Label), spaces(w - width(plain(f.Label)) + 2)}
		gap := false // something written after the label, so what follows needs three spaces
		if f.Value != "" {
			if f.Path {
				spans = append(spans, name(r.Tilde(f.Value)))
			} else {
				spans = append(spans, plain(f.Value))
			}
			gap = true
		}
		if f.Note != "" {
			if gap {
				spans = append(spans, spaces(3))
			}
			spans = append(spans, styled(f.Note, dim))
			gap = true
		}
		if f.Attention != "" {
			if gap {
				spans = append(spans, spaces(3))
			}
			spans = append(spans, r.mark(Attention), plain(" "+f.Attention))
		}
		r.write(spans...)
	}
}

// ListRow is one entry of a directory's listing: a name, a size (or another short fact), and a status.
type ListRow struct {
	Name      string // shown as a name
	Size      string // right-aligned in its column
	Status    string
	Attention bool // the status needs a decision: the ! marker before it
}

// List writes a directory's entries under a Part, indented by four, names and sizes in columns, then the status:
// "    2019-04 Council Tax.pdf   88 KiB   [!] expired 2026-11-01". Names with no size and no status (a file's tags)
// are written alone.
func (r *Renderer) List(rows []ListRow) {
	nameW, sizeW := 0, 0
	for _, row := range rows {
		nameW = max(nameW, width(name(row.Name)))
		sizeW = max(sizeW, width(plain(row.Size)))
	}
	for _, row := range rows {
		spans := []span{spaces(4), name(row.Name)}
		if sizeW == 0 && row.Status == "" && !row.Attention {
			r.write(spans...) // names alone (a file's tags): no columns to line up after them
			continue
		}
		spans = append(spans, spaces(nameW-width(name(row.Name))+3), spaces(sizeW-width(plain(row.Size))),
			plain(row.Size))
		if row.Status != "" || row.Attention {
			spans = append(spans, spaces(3))
		}
		if row.Attention {
			spans = append(spans, r.mark(Attention), plain(" "))
		}
		spans = append(spans, plain(row.Status))
		r.write(spans...)
	}
}

// Block writes the head of one directory's block in a recursive listing, indented by two: its path as a name, its
// facts, and a note that needs attention: "  bills/scans/   2 files · ! 2 need a decision".
func (r *Renderer) Block(path string, attention string, facts ...string) {
	spans := []span{spaces(2), styledName(path, bold)}
	if len(facts) > 0 {
		spans = append(spans, plain("   "+r.Join(facts...)))
	}
	if attention != "" {
		if len(facts) > 0 {
			spans = append(spans, plain(r.sep()))
		} else {
			spans = append(spans, plain("   "))
		}
		spans = append(spans, r.mark(Attention), plain(" "+attention))
	}
	r.write(spans...)
}
