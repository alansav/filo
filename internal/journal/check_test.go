package journal

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// journalOf makes a journal of n lines (line 1 and n-1 test lines) for a new device: its ID, the lines, the file.
func journalOf(t *testing.T, n int) (string, [][]byte, []byte, *Writer) {
	t.Helper()
	dev, key := testDevice(t)
	w, first, err := Start(key, dev, "laptop", t0)
	if err != nil {
		t.Fatal(err)
	}
	lines := [][]byte{first}
	for i := 2; i <= n; i++ {
		raw, err := w.Add("test", testBody{Path: "a/b", N: i}, t0)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, raw)
	}
	return dev, lines, joinLines(lines), w
}

func TestCheckAGoodJournal(t *testing.T) {
	dev, lines, data, _ := journalOf(t, 5)
	res := Check(data, dev)
	if res.Stop != nil || res.Partial {
		t.Fatalf("Check: stop %v, partial %v", res.Stop, res.Partial)
	}
	if len(res.Lines) != 5 || res.Device.Name != "laptop" || res.End.Offset != int64(len(data)) {
		t.Fatalf("Check: %d lines, name %q, end %d of %d", len(res.Lines), res.Device.Name, res.End.Offset, len(data))
	}
	for i, l := range res.Lines {
		if !bytes.Equal(l.Raw, lines[i]) || l.Seq != int64(i+1) {
			t.Errorf("line %d is not as written", i+1)
		}
	}
}

func TestCheckStops(t *testing.T) {
	dev, lines, _, _ := journalOf(t, 4)
	_, okey := testDevice(t)
	_, olines, _, _ := journalOf(t, 3)
	forged, err := Encode(okey, Header{Dev: dev, Seq: 3, Prev: HashOf(lines[1]), Kind: "test"}, testBody{N: 99})
	if err != nil {
		t.Fatal(err)
	}
	tamper := bytes.Replace(lines[2], []byte(`"n":3`), []byte(`"n":4`), 1)
	newer, _ := Encode(okey, Header{V: 2, Dev: dev, Seq: 3, Prev: HashOf(lines[1]), Kind: "test"}, nil)
	again, _ := Encode(okey, Header{Dev: dev, Seq: 3, Prev: HashOf(lines[1]), Kind: KindDevice}, nil)
	tests := []struct {
		name  string
		data  [][]byte
		dev   string
		lines int // accepted before the stop
	}{
		{"another device's journal under this name", olines, dev, 0},
		{"a forged line, signed with another key", [][]byte{lines[0], lines[1], forged}, dev, 2},
		{"an edited line", [][]byte{lines[0], lines[1], tamper, lines[3]}, dev, 2},
		{"a line removed", [][]byte{lines[0], lines[2], lines[3]}, dev, 1},
		{"two lines swapped", [][]byte{lines[0], lines[2], lines[1]}, dev, 1},
		{"a line replayed", [][]byte{lines[0], lines[1], lines[1]}, dev, 2},
		{"line 1 missing", [][]byte{lines[1], lines[2]}, dev, 0},
		{"a newer format", [][]byte{lines[0], lines[1], newer, lines[2]}, dev, 2},
		{"the device again", [][]byte{lines[0], lines[1], again}, dev, 2},
		{"a line of another journal", [][]byte{lines[0], olines[1]}, dev, 1},
	}
	for _, tt := range tests {
		res := Check(joinLines(tt.data), tt.dev)
		if res.Stop == nil {
			t.Errorf("%s: no stop", tt.name)
			continue
		}
		if len(res.Lines) != tt.lines {
			t.Errorf("%s: %d lines accepted, want %d (%v)", tt.name, len(res.Lines), tt.lines, res.Stop)
		}
	}
	if res := Check(joinLines([][]byte{lines[0], lines[1], newer}), dev); !errors.Is(res.Stop, ErrNewer) {
		t.Errorf("a newer format: stop %v, want ErrNewer", res.Stop)
	}
}

func TestCheckWaitsForAnIncompleteLine(t *testing.T) {
	dev, lines, data, _ := journalOf(t, 3)
	half := data[:len(data)-1] // the last line without its newline
	res := Check(half, dev)
	if res.Stop != nil || !res.Partial || len(res.Lines) != 2 {
		t.Fatalf("a half-written last line: stop %v, partial %v, %d lines", res.Stop, res.Partial, len(res.Lines))
	}
	cut := data[:len(lines[0])+1+len(lines[1])/2]
	if res := Check(cut, dev); res.Stop != nil || !res.Partial || len(res.Lines) != 1 {
		t.Fatalf("a line cut in half: stop %v, partial %v, %d lines", res.Stop, res.Partial, len(res.Lines))
	}
	if res := Check(data[:10], dev); res.Stop != nil || !res.Partial || len(res.Lines) != 0 {
		t.Fatalf("line 1 cut: stop %v, partial %v", res.Stop, res.Partial)
	}
	if res := Check(nil, dev); res.Stop != nil || res.Partial {
		t.Fatalf("an empty journal: stop %v, partial %v", res.Stop, res.Partial)
	}
	// A run of bytes longer than a line can be, with no newline, is refused, not waited for.
	long := append(append([]byte(nil), data...), strings.Repeat("x", MaxLine+2)...)
	if res := Check(long, dev); res.Stop == nil {
		t.Fatal("an endless last line is waited for")
	}
}

func TestCheckFrom(t *testing.T) {
	dev, lines, data, w := journalOf(t, 3)
	res := Check(data, dev)
	more, err := w.Add("test", testBody{N: 4}, t0)
	if err != nil {
		t.Fatal(err)
	}
	full := append(append([]byte(nil), data...), append(more, '\n')...)
	p := res.End
	tail := full[p.Offset-int64(len(p.Last.Raw))-1:]
	got, err := CheckFrom(tail, p)
	if err != nil || got.Stop != nil {
		t.Fatalf("CheckFrom: %v, stop %v", err, got.Stop)
	}
	if len(got.Lines) != 1 || !bytes.Equal(got.Lines[0].Raw, more) || got.End.Offset != int64(len(full)) {
		t.Fatalf("CheckFrom read %d lines, end %d of %d", len(got.Lines), got.End.Offset, len(full))
	}
	// The line before the point is gone or changed: the journal must be read again from the start.
	changed := joinLines([][]byte{lines[0], lines[1], more})
	if _, err := CheckFrom(changed[len(lines[0])+1:], p); !errors.Is(err, ErrChanged) {
		t.Fatalf("CheckFrom on a changed journal: %v, want ErrChanged", err)
	}
	if _, err := CheckFrom(nil, p); !errors.Is(err, ErrChanged) {
		t.Fatalf("CheckFrom on a shortened journal: %v, want ErrChanged", err)
	}
}

func TestResume(t *testing.T) {
	dev, _, data, _ := journalOf(t, 2)
	res := Check(data, dev)
	_, other := testDevice(t)
	if _, err := Resume(other, res.End.Last); err == nil {
		t.Fatal("Resume with another device's key succeeded")
	}
}
