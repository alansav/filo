package tagindex

import (
	"flag"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the golden files in testdata")

// golden compares got with testdata/name, or rewrites it with -update-golden.
func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test -update-golden to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs (go test -update-golden rewrites it); got:\n%s", p, got)
	}
}

// joined is every page but the stylesheet, by path, each after a "== path" line.
func joined(p Pages) string {
	var b strings.Builder
	b.WriteString("dirs: " + strings.Join(p.Dirs, " ") + "\n")
	for _, name := range slices.Sorted(maps.Keys(p.Files)) {
		if strings.HasSuffix(name, "/"+Style) {
			b.WriteString("== " + name + " (the stylesheet)\n")
			continue
		}
		b.WriteString("== " + name + "\n" + string(p.Files[name]))
	}
	return b.String()
}

func TestRender(t *testing.T) {
	d, err := Build(bills, sample(t))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "sample.txt", joined(p))
	if css, err := assets.ReadFile("assets/style.css"); err != nil || string(p.Files[".index/style.css"]) != string(css) {
		t.Errorf("stylesheet not written as embedded (%v)", err)
	}
	again, _ := Render(d)
	if joined(again) != joined(p) {
		t.Error("rendering the same data twice differs")
	}
	for name, b := range p.Files {
		if strings.Contains(string(b), "http://") || strings.Contains(string(b), "https://") ||
			strings.Contains(string(b), "<script") {
			t.Errorf("%s reaches out or runs a script", name)
		}
	}
}

func TestRenderEmpty(t *testing.T) {
	d, _ := Build(bills, nil)
	p, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "empty.txt", joined(p))
}

// Names and tags are untrusted: every text is escaped for where it lands, and links stay relative.
func TestRenderEscapes(t *testing.T) {
	when := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	files := []store.IndexFile{
		file(t, `bills/<script>alert(1)</script> "q" & 'a'.pdf`, "", when, `sender:<b>Bold</b> & "Co"`),
		file(t, "bills/javascript:alert(1)", "", when, "type:bill"),
		file(t, "bills/a #1 ?x=%41 100%.pdf", "", when, "type:bill"),
		file(t, "bills/dir with space/b.pdf", "", when, "type:bill"),
	}
	d, err := Build(bills, files)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	all := joined(p)
	for _, bad := range []string{"<script>", "<b>Bold", `href="javascript`, `href="./javascript`, "#ZgotmplZ"} {
		if strings.Contains(all, bad) {
			t.Errorf("pages hold %q", bad)
		}
	}
	page := string(p.Files[".index/type/"+PageName(tag(t, "type:bill"))])
	for _, want := range []string{
		`href="../../javascript:alert%281%29"`,
		`href="../../a%20%231%20%3Fx=%2541%20100%25.pdf"`,
		`href="../../dir%20with%20space/b.pdf"`,
		`>a #1 ?x=%41 100%.pdf<`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("type:bill's page lacks %s:\n%s", want, page)
		}
	}
	golden(t, "escapes.txt", all)
}
