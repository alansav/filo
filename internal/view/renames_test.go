package view

import "testing"

func TestRenames(t *testing.T) {
	rows := []RenameRow{
		{Root: "/home/u/Documents", Dir: "received/2026/10", Old: "Britsh Gas bill.pdf", New: "British Gas bill.pdf",
			Symlinks: []string{"/home/u/Desktop/bill"}},
		{Root: "/home/u/Documents", Dir: "received/2026/10", Old: "notes.txt ", New: "notes.txt",
			Note: "only spaces changed"},
		{Root: "/home/u/Documents", Dir: "", Old: ` "q".txt`, New: "q.txt"},
		{Root: "/media/archive", Dir: "tax", Old: "2023 return.pdf", New: "2023 return.pd", Note: "extension changed"},
		{Root: "/home/u/Documents", Dir: "x", Old: "evil\x1b[2K.txt", New: "fine.txt"},
	}
	for _, tc := range []struct {
		name  string
		width int
		max   int
		want  string
	}{
		{"piped", 0, 0, "" +
			"    received/2026/10/\n" +
			"      Britsh Gas bill.pdf  ->  British Gas bill.pdf\n" +
			"        -> the symlink ~/Desktop/bill points here and would break\n" +
			"      \"notes.txt \"         ->  notes.txt   (only spaces changed)\n" +
			"    ./\n" +
			"      \" \\\"q\\\".txt\"         ->  q.txt\n" +
			"    /media/archive/tax/\n" +
			"      2023 return.pdf      ->  2023 return.pd   (extension changed)\n" +
			"    x/\n" +
			"      evil\\x1b[2K.txt      ->  fine.txt\n"},
		{"cut", 0, 1, "" +
			"    received/2026/10/\n" +
			"      Britsh Gas bill.pdf  ->  British Gas bill.pdf\n" +
			"        -> the symlink ~/Desktop/bill points here and would break\n" +
			"    ... 4 more files\n"},
		{"narrow", 40, 1, "" +
			"    received/2026/10/\n" +
			"      Britsh Gas bill.pdf\n" +
			"        ->  British Gas bill.pdf\n" +
			"        -> the symlink ~/Desktop/bill points here and would break\n" +
			"    ... 4 more files\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, b := plainView()
			if tc.width > 0 {
				r, b = termView(tc.width, false)
			}
			r.Heading("Update", Where{Roots: []string{"/home/u/Documents"}})
			b.Reset()
			r.Renames(RenameList{Rows: rows, Max: tc.max})
			if b.String() != tc.want {
				t.Errorf("got\n%s\nwant\n%s", b.String(), tc.want)
			}
		})
	}
}
