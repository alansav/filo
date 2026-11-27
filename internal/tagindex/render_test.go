package tagindex

import (
	"encoding/json"
	"flag"
	"html"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
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

// joined is every page but the stylesheet, by path, each after a "== path" line; the find page with its inline
// styles and script (the embedded assets, checked by TestRenderFindPage) left out.
func joined(p Pages) string {
	var b strings.Builder
	b.WriteString("dirs: " + strings.Join(p.Dirs, " ") + "\n")
	for _, name := range slices.Sorted(maps.Keys(p.Files)) {
		if strings.HasSuffix(name, "/"+Style) {
			b.WriteString("== " + name + " (the stylesheet)\n")
			continue
		}
		page := string(p.Files[name])
		if !strings.Contains(name, "/") {
			page = abbreviate(page, "<style>", "</style>", "[the inline styles]")
			page = abbreviate(page, "<script>", "</script>", "[the find page's script]")
		}
		b.WriteString("== " + name + "\n" + page)
	}
	return b.String()
}

// abbreviate replaces the first element from open to close in page with what.
func abbreviate(page, open, close, what string) string {
	i := strings.Index(page, open)
	if i < 0 {
		return page
	}
	j := strings.Index(page[i:], close)
	if j < 0 {
		return page
	}
	return page[:i] + what + page[i+j+len(close):]
}

// inline is the text between open and the next close in page.
func inline(t *testing.T, page, open, close string) string {
	t.Helper()
	i := strings.Index(page, open)
	if i < 0 {
		t.Fatalf("no %s in the page", open)
	}
	j := strings.Index(page[i+len(open):], close)
	if j < 0 {
		t.Fatalf("no %s after %s in the page", close, open)
	}
	return page[i+len(open) : i+len(open)+j]
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
		if name == "index.html" {
			continue // the find page: TestRenderFindPage
		}
		if strings.Contains(string(b), "http://") || strings.Contains(string(b), "https://") ||
			strings.Contains(string(b), "<script") {
			t.Errorf("%s reaches out or runs a script", name)
		}
	}
}

// TestRenderFindPage: NAME.html holds the embedded script and styles as they are, allowed by the
// Content-Security-Policy by their hashes, local files for a preview, fetching from its own origin (for Safari's PDF
// viewer) and nothing else (nothing from the internet), and the data as JSON that reads back.
func TestRenderFindPage(t *testing.T) {
	d, err := Build(bills, sample(t))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	page := string(p.Files["index.html"])
	script, _ := assets.ReadFile("assets/find.js")
	style, _ := assets.ReadFile("assets/style.css")
	extra, _ := assets.ReadFile("assets/find.css")
	gotScript := inline(t, page, "<script>", "</script>")
	gotStyle := inline(t, page, "<style>", "</style>")
	if gotScript != string(script) || gotStyle != string(style)+string(extra) {
		t.Error("the script or the styles are not inlined as embedded")
	}
	policy := html.UnescapeString(inline(t, page, `http-equiv="Content-Security-Policy" content="`, `"`)) // as read
	want := "default-src 'none'; script-src " + cspHash([]byte(gotScript)) + "; style-src " +
		cspHash([]byte(gotStyle)) + "; img-src 'self' file:; media-src 'self' file:; frame-src 'self' file:; " +
		"object-src 'self' file:; connect-src 'self'; base-uri 'none'; form-action 'none'"
	if policy != want {
		t.Errorf("policy %q\nwant   %q", policy, want)
	}
	var fd findData
	if err := json.Unmarshal([]byte(inline(t, page, `<script type="application/json" id="data">`, "</script>")),
		&fd); err != nil {
		t.Fatal(err)
	}
	if len(fd.Files) != len(d.Files) || len(fd.Tags) != d.Tags() || fd.Browse != "./.index/index.html" {
		t.Errorf("data: %d files, %d tags, browse %q", len(fd.Files), len(fd.Tags), fd.Browse)
	}
	for _, f := range fd.Files {
		for _, g := range f.G {
			if g < 0 || g >= len(fd.Tags) {
				t.Errorf("%s names tag %d of %d", f.N, g, len(fd.Tags))
			}
		}
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Error("the find page names a remote address")
	}
	if n := strings.Count(page, "<script"); n != 2 {
		t.Errorf("%d scripts, want the data and the find page's own", n)
	}
}

// TestRenderParallel: the pages are rendered on several goroutines; what they are does not depend on how many.
func TestRenderParallel(t *testing.T) {
	may := time.Date(2026, 5, 1, 23, 30, 0, 0, time.UTC)
	var files []store.IndexFile
	for i := range 300 {
		n := strconv.Itoa(i)
		files = append(files, file(t, "bills/"+strconv.Itoa(i%7)+"/f"+n+".pdf", "", may,
			"sender:S"+strconv.Itoa(i%11), "type:T"+strconv.Itoa(i%5), "b"+strconv.Itoa(i%3)))
	}
	files = append(files, file(t, "bills/none.pdf", "", may))
	d, err := Build(bills, files)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	one, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GOMAXPROCS(8)
	many, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Files) < 20 || joined(one) != joined(many) {
		t.Errorf("%d pages on one processor, %d on eight; the same: %v", len(one.Files), len(many.Files),
			joined(one) == joined(many))
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
		file(t, "bills/line\u2028sep\u2029.pdf", "", when, "type:bill"),
		file(t, "bills/</script><script>x()</script>.pdf", "", when, "type:bill"),
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
	for _, bad := range []string{"<script>", "<b>Bold", `href="javascript`, `href="./javascript`, "#ZgotmplZ",
		"</script><"} {
		if strings.Contains(all, bad) {
			t.Errorf("pages hold %q", bad)
		}
	}
	// The find page's data: no name or tag can end its script, and each comes back as it was.
	find := string(p.Files["index.html"])
	data := inline(t, find, `<script type="application/json" id="data">`, "</script>")
	for _, bad := range []string{"<", ">", "&", "\u2028", "\u2029"} {
		if strings.Contains(data, bad) {
			t.Errorf("the find page's data holds %q", bad)
		}
	}
	var fd findData
	if err := json.Unmarshal([]byte(data), &fd); err != nil {
		t.Fatal(err)
	}
	// A / in a path is a directory: these names are paths, the parts of a link each escaped.
	links := map[string]string{}
	for _, f := range fd.Files {
		links[path.Join(f.D, f.N)] = f.H
	}
	for rel, h := range map[string]string{
		`<script>alert(1)</script> "q" & 'a'.pdf`: "./%3Cscript%3Ealert%281%29%3C/script%3E%20%22q%22%20&%20%27a%27.pdf",
		"javascript:alert(1)":                     "./javascript:alert%281%29",
		"line\u2028sep\u2029.pdf":                 "./line%E2%80%A8sep%E2%80%A9.pdf",
		"</script><script>x()</script>.pdf":       "./%3C/script%3E%3Cscript%3Ex%28%29%3C/script%3E.pdf",
	} {
		if got, ok := links[rel]; !ok || got != h {
			t.Errorf("file %q: link %q (listed %v), want %q", rel, got, ok, h)
		}
	}
	if fd.Tags[0].V != `<b>Bold</b> & "Co"` {
		t.Errorf("tag %q", fd.Tags[0].V)
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
