package find

import (
	"time"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/view"
)

// Render writes what filo find found, for reading in a terminal (docs/design.md, Finding files): a heading (how many
// files, their size, the search, where), then every file, never cut, by root and then path, with its modified date
// and size. where names the roots searched and, with one, the directory under it. Nothing found: the heading and,
// for a name search, a Next line about the name.
func Render(v *view.Renderer, files []File, pattern string, where view.Where) {
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
	if pattern != "" {
		facts = append(facts, "named "+pattern)
	}
	v.Heading("Found", where, facts...)
	v.Blank()
	if len(files) == 0 {
		if pattern != "" {
			v.Next("Next: check the name: * matches any run of characters, and case is ignored")
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

// note says what else a reader should know about a file: that it could not be read, and its other links.
func note(f File) string {
	n := ""
	if f.Unreadable {
		n = "could not be read"
	}
	if f.Links > 0 {
		if n != "" {
			n += "; "
		}
		n += "and " + report.Plural(int64(f.Links), "hard link", "hard links") + ", the same file"
	}
	return n
}
