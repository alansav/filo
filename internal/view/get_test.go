package view

import "testing"

// filo get's output: one value; names and values; records, escaped or raw for a pipe.
func TestGetOutput(t *testing.T) {
	r, b := plainView()
	r.Line("7y")
	r.Pairs([][2]string{{"retention", "7y"}, {"keep_until", ""}})
	r.Record([]string{"7y", "2031-03-31"}, "/home/u/a\tb\nc.pdf ", false)
	r.Record([]string{"/data/a\tb"}, "/home/u/a\tb\nc.pdf ", true)
	want := "7y\nretention\t7y\nkeep_until\t\n7y\t2031-03-31\t/home/u/a\\tb\\nc.pdf\\x20\n" +
		"/data/a\\tb\t/home/u/a\tb\nc.pdf \x00"
	if b.String() != want {
		t.Errorf("piped:\ngot  %q\nwant %q", b.String(), want)
	}
	tr, tb := termView(80, false)
	tr.Pairs([][2]string{{"retention", "7y"}, {"file_date", "2024-03-31"}})
	tr.Record(nil, "x\x1b", true)
	if want := "retention   7y\nfile_date   2024-03-31\nx\\x1b\n"; tb.String() != want {
		t.Errorf("terminal:\ngot  %q\nwant %q", tb.String(), want)
	}
}
