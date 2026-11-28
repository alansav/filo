// Package tagindex writes the tag index pages (docs/design.md, Tag index pages): HTML pages listing the files under a
// directory by tag, kept up to date by Filo. It is the only code that writes or removes an index's files, and it
// replaces or removes one only while it is still exactly what Filo wrote.
package tagindex

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

// layout is the version of the pages' layout: it is part of the data's hash, so a Filo whose pages look different
// writes them again even where the register has not changed. Raise it with any change to the templates or the
// stylesheet.
const layout = 2

// BareDir is the directory of the bare tags' pages: a key has a letter, so no key's directory is named so.
const BareDir = "_"

// File is one file an index lists.
type File struct {
	Rel  string     // relative to the index's directory, '/'-separated
	Date string     // YYYY-MM-DD: its file_date, else the day it was modified (UTC, so every device writes the same)
	Tags []tags.Tag // in the order filo tags lists them
}

// Value is one tag and its files.
type Value struct {
	Tag   tags.Tag
	Page  string // its page, relative to the pages directory: "sender/british-gas-1a2b3c4d.html"
	Files []int  // positions in Data.Files
}

// Key is one key and its values, or the bare tags (Key "").
type Key struct {
	Key    string
	Dir    string  // its pages' directory, relative to the pages directory: the key, or BareDir
	Values []Value // by folded value
	Files  int     // how many files have one of its values
	None   []int   // positions in Data.Files of the files with none of its values (not for the bare tags)
}

// Data is everything an index's pages show; its hash says whether they need writing.
type Data struct {
	Layout   int
	Name     string // the index's name: NAME.html and .NAME/
	Title    string // its directory's name
	Files    []File // newest first, then by path
	Keys     []Key  // by key
	Bare     Key    // the bare tags
	Untagged []int  // positions in Data.Files of the files with no tag
}

// Hash is the data's SHA-256, hex.
func (d Data) Hash() string {
	b, err := json.Marshal(d)
	if err != nil {
		panic(err) // only strings, numbers and slices: Marshal cannot fail
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Build makes an index's data from the files under its directory (store.IndexFiles).
func Build(ix store.TagIndex, files []store.IndexFile) (Data, error) {
	d := Data{Layout: layout, Name: ix.Name, Title: title(ix)}
	for _, f := range files {
		rel := f.Rel
		if ix.Dir != "" {
			rel = strings.TrimPrefix(rel, ix.Dir+"/")
		}
		date := f.FileDate
		if date == "" {
			date = time.Unix(0, f.MtimeNS).UTC().Format(time.DateOnly)
		}
		d.Files = append(d.Files, File{Rel: rel, Date: date, Tags: f.Tags})
	}
	slices.SortFunc(d.Files, func(a, b File) int {
		if c := cmp.Compare(b.Date, a.Date); c != 0 {
			return c
		}
		return cmp.Compare(a.Rel, b.Rel)
	})

	keys := map[string]*Key{}
	values := map[string]map[string]*Value{} // key -> folded value -> value
	for i, f := range d.Files {
		if len(f.Tags) == 0 {
			d.Untagged = append(d.Untagged, i)
		}
		seen := map[string]bool{}
		for _, t := range f.Tags {
			k := keys[t.Key]
			if k == nil {
				k = &Key{Key: t.Key, Dir: keyDir(t.Key)}
				keys[t.Key] = k
				values[t.Key] = map[string]*Value{}
			}
			if !seen[t.Key] {
				seen[t.Key] = true
				k.Files++
			}
			v := values[t.Key][t.Folded()]
			if v == nil {
				v = &Value{Tag: t}
				values[t.Key][t.Folded()] = v
			}
			v.Files = append(v.Files, i)
		}
	}
	for key, k := range keys {
		pages := map[string]tags.Tag{}
		for _, v := range values[key] {
			v.Page = path.Join(k.Dir, PageName(v.Tag))
			if other, ok := pages[v.Page]; ok {
				return Data{}, fmt.Errorf("the tags %s and %s would have the same page, %s", other, v.Tag, v.Page)
			}
			pages[v.Page] = v.Tag
			k.Values = append(k.Values, *v)
		}
		slices.SortFunc(k.Values, func(a, b Value) int { return cmp.Compare(a.Tag.Folded(), b.Tag.Folded()) })
		if key != "" {
			for i, f := range d.Files {
				if !slices.ContainsFunc(f.Tags, func(t tags.Tag) bool { return t.Key == key }) {
					k.None = append(k.None, i)
				}
			}
		}
	}
	dirs := map[string]string{} // a key's directory, in lower case -> the key
	for key, k := range keys {
		if other, ok := dirs[strings.ToLower(k.Dir)]; ok {
			return Data{}, fmt.Errorf("the keys %s and %s would have the same directory, %s", other, key, k.Dir)
		}
		dirs[strings.ToLower(k.Dir)] = key
	}
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		if key == "" {
			d.Bare = *keys[key]
		} else {
			d.Keys = append(d.Keys, *keys[key])
		}
	}
	if d.Bare.Dir == "" {
		d.Bare.Dir = BareDir
	}
	return d, nil
}

// title is the name of the index's directory (the root's, for an index on the root itself).
func title(ix store.TagIndex) string {
	if ix.Dir != "" {
		return path.Base(ix.Dir)
	}
	return path.Base(strings.ReplaceAll(ix.RootPath, "\\", "/"))
}

// keyDir is the directory of a key's pages: BareDir for the bare tags; the key itself when it is plain ASCII (a-z,
// 0-9, - and _: keys are lower case already); otherwise a slug of it, a dot, and eight hex digits of the key's
// SHA-256 ("caf.1a2b3c4d"). A key may hold any letter, and two keys that differ only in their Unicode form (é as one
// character or two) are one name on some file systems; no key holds a dot, so these names are never a key's.
func keyDir(key string) string {
	if key == "" {
		return BareDir
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			sum := sha256.Sum256([]byte(key))
			s := slug(key)
			if s == "" {
				s = "k"
			}
			return s + "." + hex.EncodeToString(sum[:4])
		}
	}
	return key
}

// PageName is the name of a tag's page in its key's directory: a slug of its value (a-z, 0-9 and -, at most 40) and
// eight hex digits of the SHA-256 of its key and folded value, so different tags whose slugs are the same have
// different pages, and a tag keeps its page through a rename of its case (docs/design.md, Tag index pages, "Pages").
// It never starts with _, nor is it index.html.
func PageName(t tags.Tag) string {
	sum := sha256.Sum256([]byte(t.Key + "\x00" + t.Folded()))
	h := hex.EncodeToString(sum[:4])
	if s := slug(t.Value); s != "" {
		return s + "-" + h + ".html"
	}
	return h + ".html"
}

// slug is s in lower case ASCII letters and digits, every other run of characters one -, none at either end, cut to
// at most 40 characters.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	out := b.String()
	if len(out) > 40 {
		out = strings.TrimRight(out[:40], "-")
	}
	return out
}
