package view

import "testing"

func TestMoves(t *testing.T) {
	rows := []MoveRow{
		{Root: "/home/u/Documents", Dir: "received/2026/11", New: true, From: "inbox/bill.pdf"},
		{Root: "/home/u/Documents", Dir: "received/2026/11", New: true, From: "inbox/scan 2.pdf",
			Name: "scan 2 (council).pdf", Symlinks: []string{"/home/u/Desktop/scan"}},
		{Root: "/home/u/Documents", Dir: "", From: "inbox/notes.txt ", Name: "notes.txt", Note: "only spaces changed"},
		{Root: "/media/archive", Dir: "tax", From: "2023 return.pdf"},
	}
	for _, tc := range []struct {
		name  string
		width int
		max   int
		want  string
	}{
		{"piped", 0, 0, "" +
			"    received/2026/11/   (new)\n" +
			"      inbox/bill.pdf\n" +
			"      inbox/scan 2.pdf   as  scan 2 (council).pdf\n" +
			"        -> the symlink ~/Desktop/scan points here and would break\n" +
			"    ./\n" +
			"      inbox/notes.txt\\x20   as  notes.txt   (only spaces changed)\n" +
			"    /media/archive/tax/\n" +
			"      /media/archive/2023 return.pdf\n"},
		{"cut", 0, 1, "" +
			"    received/2026/11/   (new)\n" +
			"      inbox/bill.pdf\n" +
			"    ... 3 more files\n"},
		{"narrow", 30, 2, "" +
			"    received/2026/11/   (new)\n" +
			"      inbox/bill.pdf\n" +
			"      inbox/scan 2.pdf\n" +
			"        as  scan 2 (council).pdf\n" +
			"        -> the symlink ~/Desktop/scan points here and would break\n" +
			"    ... 2 more files\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, b := plainView()
			if tc.width > 0 {
				r, b = termView(tc.width, false)
			}
			r.Heading("Update", Where{Roots: []string{"/home/u/Documents"}})
			b.Reset()
			r.Moves(MoveList{Rows: rows, Max: tc.max})
			if b.String() != tc.want {
				t.Errorf("got\n%s\nwant\n%s", b.String(), tc.want)
			}
		})
	}
}
