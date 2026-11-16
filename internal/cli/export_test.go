package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestExportGolden: filo export's whole plain output (docs/design.md, Terminal output, Export): a retention export
// of a folder with a file_date read from a file name, one with nothing to export, and a duplicates export. The CSVs
// themselves are tested in the round trips.
func TestExportGolden(t *testing.T) {
	ct := newCLITest(t)
	setNow(t, time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local))
	write, filo, in := ct.write, ct.filo, ct.in
	write("tax/2023-03-14 statement.pdf", "statement")
	write("tax/notes.txt", "notes")
	write("photos/a.jpg", "photo")
	write("photos/a copy.jpg", "photo")
	if err := os.MkdirAll(in("empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	filo(ExitOK, "", false, "roots", "add", ct.root)
	var out strings.Builder
	step := func(title string, args ...string) {
		t.Helper()
		out.WriteString("$ filo " + title + "\n" + filo(ExitOK, "", false, args...).stdout + "\n")
	}
	csv := func(name string) string { return filepath.Join(ct.work, name) }

	step("export retention tax", "export", "retention", in("tax"), "-o", csv("l.csv"))
	step("export retention empty", "export", "retention", in("empty"), "-o", csv("e.csv"))
	step("export duplicates", "export", "duplicates", "-o", csv("my d.csv"))

	got := strings.ReplaceAll(out.String(), ct.root, "<root>") // the root first: it is inside the work folder
	got = strings.ReplaceAll(got, ct.work, "<work>")
	golden(t, "export.txt", ct.root, got)
}
