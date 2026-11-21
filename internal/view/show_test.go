package view

import "testing"

// The parts of filo show: a title line, parts indented under it, fields lined up under each part.
func TestSubjectPartFields(t *testing.T) {
	tests := []struct {
		name string
		draw func(r *Renderer)
		want string
	}{
		{"one file: no counter", func(r *Renderer) {
			r.Subject(Subject{N: 1, Total: 1, Name: "gas.pdf", Status: "kept until 2029-01-31",
				Facts: []string{"184 KiB"}, Dir: "/home/u/Documents/bills"})
		}, "gas.pdf   kept until 2029-01-31 . 184 KiB . ~/Documents/bills\n"},
		{"several: the counter, and attention", func(r *Renderer) {
			r.Subject(Subject{N: 2, Total: 3, Name: "water.pdf", Status: "needs a retention", Attention: true,
				Facts: []string{"61 KiB"}, Dir: "/data/bills"})
		}, "[2/3] water.pdf   [!] needs a retention . 61 KiB . /data/bills\n"},
		{"a name with a trailing space", func(r *Renderer) {
			r.Subject(Subject{Name: "report ", Status: "kept forever"})
		}, `report\x20   kept forever` + "\n"},
		{"part", func(r *Renderer) {
			r.Part("File")
			r.Part("Copies", "1 other file with the same content", "counted as a duplicate")
		}, "  File\n  Copies   1 other file with the same content . counted as a duplicate\n"},
		{"fields line up after the longest label", func(r *Renderer) {
			r.Fields([]Field{
				{Label: "path", Value: "/home/u/Documents/gas.pdf", Path: true, Attention: "not on disk"},
				{Label: "modified", Value: "2022-02-03 09:14", Note: "unchanged since the last scan (2026-11-20)"},
				{Label: "first seen", Value: "2026-11-12 (scan 3)"},
				{Label: "status", Attention: "undated: no retention"},
				{Label: "keep_until", Value: "2020-01-31", Note: "x", Attention: "1 year ago"},
			})
		}, "    path        ~/Documents/gas.pdf   [!] not on disk\n" +
			"    modified    2022-02-03 09:14   unchanged since the last scan (2026-11-20)\n" +
			"    first seen  2026-11-12 (scan 3)\n" +
			"    status      [!] undated: no retention\n" +
			"    keep_until  2020-01-31   x   [!] 1 year ago\n"},
		{"a path value is escaped as a name", func(r *Renderer) {
			r.Fields([]Field{{Label: "path", Value: "/x/a\x1b[2Jb", Path: true}})
		}, `    path  /x/a\x1b[2Jb` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, b := plainView()
			tt.draw(r)
			if b.String() != tt.want {
				t.Errorf("got\n%q\nwant\n%q", b.String(), tt.want)
			}
		})
	}
}

// A Subject starts a new thing: a heading's root no longer shortens the paths after it.
func TestSubjectResetsTheRoot(t *testing.T) {
	r, b := plainView()
	r.Heading("Ready", Where{Roots: []string{"/data"}})
	r.Subject(Subject{Name: "a", Status: "kept"})
	r.Files(FileList{Rows: []FileRow{{Root: "/data", Rel: "b/a", Note: "until 2029-01-31"}}})
	want := "Ready   /data\na   kept\n    /data/b/a   until 2029-01-31\n"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}
}
