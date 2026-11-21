package find

import (
	"bytes"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

func TestRender(t *testing.T) {
	day := func(d int) int64 { return time.Date(2026, 10, d, 12, 0, 0, 0, time.Local).UnixNano() }
	docs, archive := "/home/u/Documents", "/mnt/archive"
	file := func(root, rel string, size int64, d int) File {
		return File{FoundFile: store.FoundFile{Root: root, Rel: rel, Size: size, MtimeNS: day(d)}}
	}
	bill := file(docs, "received/2026/10/gas bill.pdf", 188416, 3)
	locked := file(docs, "house/gas safety.pdf", 2048, 4)
	locked.Unreadable, locked.Links = true, 1
	old := file(archive, "tax/2018 return.pdf", 500, 5)
	for _, tc := range []struct {
		name    string
		files   []File
		pattern string
		where   view.Where
		before  string
		want    string
	}{
		{"one root", []File{locked, bill}, "*gas*", view.Where{Roots: []string{docs}}, "", "" +
			"Found   2 files . 186 KiB . named *gas* . ~/Documents\n" +
			"\n" +
			"    modified        size\n" +
			"    2026-10-04   2.0 KiB   house/gas safety.pdf   could not be read; and 1 hard link, the same file\n" +
			"    2026-10-03   184 KiB   received/2026/10/gas bill.pdf\n"},
		{"under a directory, no name", []File{bill}, "", view.Where{Roots: []string{docs}, Under: "received"}, "", "" +
			"Found   1 file . 184 KiB . ~/Documents, under received/\n" +
			"\n" +
			"    modified        size\n" +
			"    2026-10-03   184 KiB   received/2026/10/gas bill.pdf\n"},
		{"several roots", []File{bill, old}, "*.pdf", view.Where{Roots: []string{docs, archive}}, "", "" +
			"Found   2 files . 184 KiB . named *.pdf . 2 roots\n" +
			"\n" +
			"    modified        size\n" +
			"  ~/Documents\n" +
			"    2026-10-03   184 KiB   received/2026/10/gas bill.pdf\n" +
			"  /mnt/archive\n" +
			"    2026-10-05     500 B   tax/2018 return.pdf\n"},
		{"nothing found", nil, "*gaz*", view.Where{Roots: []string{docs}}, "", "" +
			"Found   no files . named *gaz* . ~/Documents\n" +
			"\n" +
			"Next: check the name: * matches any run of characters, and case is ignored\n"},
		{"nothing at all", nil, "", view.Where{Roots: []string{docs}}, "", "" +
			"Found   no files . ~/Documents\n" +
			"\n"},
		{"not scanned first", []File{bill}, "", view.Where{Roots: []string{docs}},
			"not scanned first: as of the last scan, 2026-11-21 09:00", "" +
				"Found   1 file . 184 KiB . ~/Documents\n" +
				"  - not scanned first: as of the last scan, 2026-11-21 09:00\n" +
				"\n" +
				"    modified        size\n" +
				"    2026-10-03   184 KiB   received/2026/10/gas bill.pdf\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			Render(view.New(view.Options{Out: &b, Home: "/home/u"}), tc.files, tc.pattern, tc.where, tc.before)
			if b.String() != tc.want {
				t.Errorf("got\n%s\nwant\n%s", b.String(), tc.want)
			}
		})
	}
}
