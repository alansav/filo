package find

import (
	"strings"
	"time"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/view"
)

// Render writes what filo find found, for reading in a terminal (docs/design.md, Finding files): a heading (how many
// files, their size, the search, where), then every file, never cut, by root and then path, with its modified date
// and size, and its tags after the path. where names the roots searched and, with one, the directory under it;
// notScanned, if not "", is the line under the heading that says the list is as of an earlier scan (--no-scan).
// Nothing found: the heading and, for a search by name or tag, a Next line about it.
func Render(v *view.Renderer, files []File, q Query, where view.Where, notScanned string) {
	var facts []string
	if len(files) == 0 {
		facts = append(facts, "no files")
	} else {
		var size int64
		for _, f := range files {
			size += f.Size
		}
		facts = append(facts, report.Plural(int64(len(files)), "file", "files"), report.Size(size))
	}
	if q.Name != "" {
		facts = append(facts, "named "+q.Name)
	}
	for _, t := range q.Tagged {
		facts = append(facts, "tagged "+t.String())
	}
	for _, t := range q.NotTagged {
		facts = append(facts, "not tagged "+t.String())
	}
	v.Heading("Found", where, facts...)
	if notScanned != "" {
		v.Count(view.Neutral, notScanned)
	}
	v.Blank()
	if len(files) == 0 {
		switch {
		case q.Name != "" && len(q.Tagged)+len(q.NotTagged) > 0:
			v.Next("Next: check the name and the tags: * matches any run of characters, and case is ignored; " +
				"filo tags lists the tags")
		case q.Name != "":
			v.Next("Next: check the name: * matches any run of characters, and case is ignored")
		case len(q.Tagged)+len(q.NotTagged) > 0:
			v.Next("Next: check the tags: filo tags lists them; key: finds any value, and * any run of characters")
		}
		return
	}
	rows := make([]view.FileRow, len(files))
	for i, f := range files {
		rows[i] = view.FileRow{Root: f.Root, Rel: f.Rel, Cells: []string{modified(f.MtimeNS), report.Size(f.Size)},
			Note: note(f)}
	}
	v.Files(view.FileList{Columns: []view.Column{{Header: "modified"}, {Header: "size", Right: true}}, Rows: rows,
		ByRoot: true})
}

// modified is a file's modified date, local; "" when not known.
func modified(ns int64) string {
	if ns == 0 {
		return ""
	}
	return time.Unix(0, ns).Local().Format("2006-01-02")
}

// note says what else a reader should know about a file: its tags, then, in brackets after them so no tag can read
// as one, filo's notes: that it could not be read, and its other links.
func note(f File) string {
	var notes []string
	if f.Unreadable {
		notes = append(notes, "could not be read")
	}
	if f.Links > 0 {
		notes = append(notes, "and "+report.Plural(int64(f.Links), "hard link", "hard links")+", the same file")
	}
	own := strings.Join(notes, "; ")
	if len(f.Tags) == 0 {
		return own
	}
	names := make([]string, len(f.Tags))
	for i, t := range f.Tags {
		names[i] = t.String()
	}
	if own != "" {
		return strings.Join(names, "; ") + "   (" + own + ")"
	}
	return strings.Join(names, "; ")
}
