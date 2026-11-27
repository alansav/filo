package tagindex

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/alansav/filo/internal/tags"
)

//go:embed assets/page.html assets/style.css assets/find.html assets/find.css assets/find.js
var assets embed.FS

var (
	pageTemplate = template.Must(template.ParseFS(assets, "assets/page.html"))
	findTemplate = template.Must(template.ParseFS(assets, "assets/find.html"))
)

// Style is the stylesheet every page links to, in the pages directory.
const Style = "style.css"

// Untagged and None are the pages of the files with no tag, in the pages directory, and of the files with none of a
// key's values, in its directory: a tag's page never starts with _ (PageName).
const (
	Untagged = "_untagged.html"
	None     = "_none.html"
	KeyPage  = "index.html" // a key's page, in its directory
)

type link struct{ Text, Href string }

type row struct{ Text, Href, Count string }

type fileRow struct {
	Name, Href, Dir, Date string
	Tags                  []link
}

type page struct {
	Title, Style, Find, Heading, Counts, Empty string
	Trail                                      []link
	Rows                                       []row
	Files                                      []fileRow
	Note                                       *link
}

// Pages are an index's rendered files, by path relative to its directory ('/'-separated), and the directories that
// hold them.
type Pages struct {
	Files map[string][]byte
	Dirs  []string // parents first
}

// Render makes every page of an index from its data: NAME.html, the find page, and in .NAME/ the stylesheet, the
// list of keys, a page per key (and one for the bare tags), a page per tag, and the pages of the files with no tag
// and with none of a key's values, where there are any (docs/design.md, Tag index pages, "Pages" and "The find
// page").
func Render(d Data) (Pages, error) {
	r := renderer{d: d, dir: "." + d.Name, out: Pages{Files: map[string][]byte{}}, pages: map[string]string{}}
	for _, k := range append(append([]Key(nil), d.Keys...), d.Bare) {
		for _, v := range k.Values {
			r.pages[v.Tag.Key+"\x00"+v.Tag.Folded()] = v.Page
		}
	}
	style, err := assets.ReadFile("assets/style.css")
	if err != nil {
		return Pages{}, err
	}
	r.out.Dirs = append(r.out.Dirs, r.dir)
	r.out.Files[r.dir+"/"+Style] = style
	find, err := r.find(style)
	if err != nil {
		return Pages{}, err
	}
	r.out.Files[d.Name+".html"] = find
	r.main()
	if len(d.Untagged) > 0 {
		p := r.base(1, link{Text: "no tags"})
		p.Heading = "No tags"
		p.Counts = count(len(d.Untagged), "file has", "files have") + " no tags"
		p.Files = r.files(1, d.Untagged, tags.Tag{}, false)
		r.write(Untagged, p)
	}
	for _, k := range append(append([]Key(nil), d.Keys...), d.Bare) {
		if len(k.Values) == 0 {
			continue
		}
		r.key(k)
	}
	if err := r.execute(); err != nil {
		return Pages{}, err
	}
	return r.out, nil
}

type renderer struct {
	d     Data
	dir   string // the pages directory, .NAME
	out   Pages
	pages map[string]string // a tag's key and folded value -> its page, relative to the pages directory
	todo  []pageJob         // the pages made, rendered together by execute
}

// pageJob is a page to render to name, a path relative to the index's directory.
type pageJob struct {
	name string
	p    page
}

// write adds p, to be rendered to rel, a path relative to the pages directory, by execute.
func (r *renderer) write(rel string, p page) {
	r.todo = append(r.todo, pageJob{name: path.Join(r.dir, rel), p: p})
}

// execute renders every page made, on as many goroutines as there are processors: rendering is most of the time
// a changed index takes, and each page stands alone. The error is the first page's that failed, in the order the
// pages were made, so it is the same on every run.
func (r *renderer) execute() error {
	out := make([][]byte, len(r.todo))
	errs := make([]error, len(r.todo))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(r.todo)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				var b bytes.Buffer
				if err := pageTemplate.Execute(&b, r.todo[i].p); err != nil {
					// Named as before: relative to the pages directory, the main page by its own name.
					errs[i] = fmt.Errorf("render %s: %w", strings.TrimPrefix(r.todo[i].name, r.dir+"/"), err)
					continue
				}
				out[i] = b.Bytes()
			}
		}()
	}
	// The longest pages first (files or rows), so that none is left to render alone at the end.
	order := make([]int, len(r.todo))
	for i := range order {
		order[i] = i
	}
	size := func(i int) int { return len(r.todo[i].p.Files) + len(r.todo[i].p.Rows) }
	slices.SortStableFunc(order, func(a, b int) int { return size(b) - size(a) })
	for _, i := range order {
		next <- i
	}
	close(next)
	wg.Wait()
	for i, j := range r.todo {
		if errs[i] != nil {
			return errs[i]
		}
		r.out.Files[j.name] = out[i]
	}
	return nil
}

// up is the way from a page depth directories below the index's directory back to it.
func up(depth int) string {
	if depth == 0 {
		return "./"
	}
	return strings.Repeat("../", depth)
}

// href is a relative link to rel, from a page depth directories below the index's directory, each part of the path
// escaped for a URL (so a # or ? in a name cannot cut it short). It starts with ./ or ../, so no name reads as a
// scheme.
func href(depth int, rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return up(depth) + strings.Join(parts, "/")
}

// base is a page depth directories below the index's directory (1: the pages directory, 2: a key's), with its trail
// from the list of keys down to trail, and its link to the find page.
func (r *renderer) base(depth int, trail ...link) page {
	p := page{Title: r.d.Title, Style: href(depth-1, Style), Find: href(depth, r.d.Name+".html")}
	p.Trail = append([]link{{Text: r.d.Title, Href: href(depth-1, KeyPage)}}, trail...)
	if last := p.Trail[len(p.Trail)-1]; last.Text != "" {
		p.Title = last.Text + " · " + r.d.Title
	}
	return p
}

// main writes the list of keys, .NAME/index.html: the static pages' first page (no key's directory is named so: a
// key holds no dot).
func (r *renderer) main() {
	p := page{Title: r.d.Title, Style: href(0, Style), Find: href(1, r.d.Name+".html"), Heading: r.d.Title,
		Trail: []link{{Text: r.d.Title}}}
	for _, k := range r.d.Keys {
		p.Rows = append(p.Rows, row{Text: k.Key, Href: href(0, k.Dir+"/"+KeyPage),
			Count: count(len(k.Values), "value", "values") + " · " + count(k.Files, "file", "files")})
	}
	if b := r.d.Bare; len(b.Values) > 0 {
		p.Rows = append(p.Rows, row{Text: "other tags", Href: href(0, b.Dir+"/"+KeyPage),
			Count: count(len(b.Values), "tag", "tags") + " · " + count(b.Files, "file", "files")})
	}
	p.Counts = count(len(r.d.Files), "file", "files") + " · " + count(r.d.Tags(), "tag", "tags")
	if len(r.d.Files) == 0 {
		p.Empty = "No files here yet."
	} else if len(p.Rows) == 0 {
		p.Empty = "No file here has a tag yet."
	}
	if n := len(r.d.Untagged); n > 0 {
		p.Note = &link{Text: count(n, "file has", "files have") + " no tags", Href: href(0, Untagged)}
	}
	r.write(KeyPage, p)
}

// findTag and findFile are the find page's data (docs/design.md, Tag index pages, "The find page"), in short names
// since a page holds every file: a tag's key, value and page (relative to the pages directory); a file's name, its
// directory (relative to the index's, "" for the index's own), its link from NAME.html, its date and its tags (their
// positions in the tags).
type findTag struct {
	K string `json:"k"`
	V string `json:"v"`
	P string `json:"p"`
}

type findFile struct {
	N string `json:"n"`
	D string `json:"d"`
	H string `json:"h"`
	T string `json:"t"`
	G []int  `json:"g"`
}

type findData struct {
	Title  string     `json:"title"`
	Browse string     `json:"browse"`
	Tags   []findTag  `json:"tags"`
	Files  []findFile `json:"files"`
}

// findPage is what the find page's template fills in. Data, Script and Style go in as they are: Data is JSON from
// encoding/json, which escapes <, > and & (so no name can end the script) and U+2028/9; Script and Style are the
// embedded assets, which the Content-Security-Policy (Policy) allows by their hashes.
type findPage struct {
	Title, Counts, Browse, Policy string
	Data, Script                  template.JS
	Style                         template.CSS
}

// find renders NAME.html, the find page, with style (the stylesheet the static pages share) and the find page's own
// styles inline.
func (r *renderer) find(style []byte) ([]byte, error) {
	extra, err := assets.ReadFile("assets/find.css")
	if err != nil {
		return nil, err
	}
	script, err := assets.ReadFile("assets/find.js")
	if err != nil {
		return nil, err
	}
	css := string(style) + string(extra)
	fd := findData{Title: r.d.Title, Browse: href(0, r.dir+"/"+KeyPage), Tags: []findTag{}, Files: []findFile{}}
	ids := map[string]int{} // a tag's key and folded value -> its position
	for _, k := range append(append([]Key(nil), r.d.Keys...), r.d.Bare) {
		for _, v := range k.Values {
			ids[v.Tag.Key+"\x00"+v.Tag.Folded()] = len(fd.Tags)
			fd.Tags = append(fd.Tags, findTag{K: v.Tag.Key, V: v.Tag.Value, P: v.Page})
		}
	}
	for _, f := range r.d.Files {
		ff := findFile{N: path.Base(f.Rel), H: href(0, f.Rel), T: f.Date, G: []int{}}
		if d := path.Dir(f.Rel); d != "." {
			ff.D = d
		}
		for _, t := range f.Tags {
			ff.G = append(ff.G, ids[t.Key+"\x00"+t.Folded()])
		}
		fd.Files = append(fd.Files, ff)
	}
	data, err := json.Marshal(fd)
	if err != nil {
		return nil, err
	}
	p := findPage{
		Title:  r.d.Title,
		Counts: count(len(r.d.Files), "file", "files") + " · " + count(r.d.Tags(), "tag", "tags"),
		Browse: fd.Browse,
		Policy: "default-src 'none'; script-src " + cspHash(script) + "; style-src " + cspHash([]byte(css)) +
			"; base-uri 'none'; form-action 'none'",
		Data:   template.JS(data),   // safe: JSON from encoding/json (see findPage)
		Script: template.JS(script), // safe: the embedded script
		Style:  template.CSS(css),   // safe: the embedded stylesheets
	}
	var b bytes.Buffer
	if err := findTemplate.Execute(&b, p); err != nil {
		return nil, fmt.Errorf("render %s.html: %w", r.d.Name, err)
	}
	return b.Bytes(), nil
}

// cspHash is a Content-Security-Policy source allowing exactly the inline script or style b.
func cspHash(b []byte) string {
	sum := sha256.Sum256(b)
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// key writes a key's page (the bare tags' for Key ""), each of its tags' pages, and its "no …" page.
func (r *renderer) key(k Key) {
	r.out.Dirs = append(r.out.Dirs, path.Join(r.dir, k.Dir))
	name := k.Key
	if name == "" {
		name = "other tags"
	}
	keyLink := link{Text: name, Href: KeyPage}
	p := r.base(2, link{Text: name})
	p.Heading = name
	p.Counts = count(len(k.Values), "value", "values") + " · " + count(k.Files, "file", "files")
	if k.Key == "" {
		p.Counts = count(len(k.Values), "tag", "tags") + " · " + count(k.Files, "file", "files")
	}
	for _, v := range k.Values {
		p.Rows = append(p.Rows, row{Text: v.Tag.Value, Href: "./" + path.Base(v.Page),
			Count: count(len(v.Files), "file", "files")})
	}
	if len(k.None) > 0 {
		p.Note = &link{Text: count(len(k.None), "file has", "files have") + " no " + k.Key + " tag", Href: "./" + None}
	}
	r.write(path.Join(k.Dir, KeyPage), p)
	for _, v := range k.Values {
		p := r.base(2, keyLink, link{Text: v.Tag.Value})
		p.Heading = v.Tag.String()
		p.Counts = count(len(v.Files), "file", "files")
		p.Files = r.files(2, v.Files, v.Tag, true)
		r.write(v.Page, p)
	}
	if len(k.None) > 0 {
		p := r.base(2, keyLink, link{Text: "no " + k.Key})
		p.Heading = "No " + k.Key + " tag"
		p.Counts = count(len(k.None), "file has", "files have") + " no " + k.Key + " tag"
		p.Files = r.files(2, k.None, tags.Tag{}, false)
		r.write(path.Join(k.Dir, None), p)
	}
}

// files are the rows of the files at positions in Data.Files, on a page depth directories below the index's
// directory, each with its tags but the page's own (skip, when skipping).
func (r *renderer) files(depth int, positions []int, own tags.Tag, skip bool) []fileRow {
	var out []fileRow
	for _, i := range positions {
		f := r.d.Files[i]
		fr := fileRow{Name: path.Base(f.Rel), Href: href(depth, f.Rel), Date: f.Date}
		if d := path.Dir(f.Rel); d != "." {
			fr.Dir = d
		}
		for _, t := range f.Tags {
			if skip && t.Same(own) {
				continue
			}
			fr.Tags = append(fr.Tags, link{Text: t.String(), Href: up(depth-1) + r.pages[t.Key+"\x00"+t.Folded()]})
		}
		out = append(out, fr)
	}
	return out
}

// count is n with the word for one or many.
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
