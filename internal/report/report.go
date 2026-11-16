// Package report builds the `filo report` overview from the register and renders it with the terminal output
// components.
package report

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// Report is everything `filo report` shows.
type Report struct {
	Roots       []store.RootTotal
	Largest     []store.FileRow
	Duplicates  store.Duplicates
	Broken      []store.LinkRow
	BrokenTotal int64
	Unreadable  []store.UnreadableRow
	UnreadTotal int64
	Events      []store.EventRow
	Status      map[store.Status]int64 // present files by lifespan status
	Undecided   int64                  // files that need a keep_until decision
	UndecidedIn []DirGroup             // the directories holding most of them
	Deleted     store.DeletedTotals    // what filo has deleted
	Snapshots   store.SnapshotInfo     // copies of the register
}

// DirDepth is how many levels below a root the report groups files needing a decision by.
const DirDepth = 2

// StatusLabel names a lifespan status for people.
func StatusLabel(s store.Status) string {
	switch s {
	case store.StatusUnreadable:
		return "unreadable"
	case store.StatusPossibleMove:
		return "possible moves"
	case store.StatusNeedsFileDate:
		return "need a file_date for their policy"
	case store.StatusUndated:
		return "no retention"
	case store.StatusChanged:
		return "past retention, changed since it was set"
	case store.StatusExpired:
		return "past retention"
	case store.StatusKept:
		return "kept"
	case store.StatusForever:
		return "kept forever"
	}
	return string(s)
}

// Build reads the report from the register, with top entries in each list. today (YYYY-MM-DD) decides which
// keep_until dates have passed.
func Build(ctx context.Context, s *store.Store, top int, today string) (Report, error) {
	var r Report
	var err error
	if r.Roots, err = s.RootTotals(ctx); err != nil {
		return r, err
	}
	files, err := s.Files(ctx, today, store.Filter{})
	if err != nil {
		return r, err
	}
	r.Status = map[store.Status]int64{}
	var undecided []store.FileItem
	for _, it := range files {
		r.Status[it.Status]++
		if it.Status.NeedsDecision() {
			undecided = append(undecided, it)
		}
	}
	r.Undecided = int64(len(undecided))
	r.UndecidedIn = DirGroups(undecided, DirDepth, top)
	if r.Largest, err = s.Largest(ctx, top); err != nil {
		return r, err
	}
	if r.Duplicates, err = s.Duplicates(ctx, top); err != nil {
		return r, err
	}
	if r.Broken, r.BrokenTotal, err = s.BrokenLinks(ctx, top); err != nil {
		return r, err
	}
	if r.Unreadable, r.UnreadTotal, err = s.UnreadableFiles(ctx, top); err != nil {
		return r, err
	}
	if r.Events, err = s.RecentEvents(ctx, top); err != nil {
		return r, err
	}
	if r.Deleted, err = s.Deleted(ctx); err != nil {
		return r, err
	}
	if r.Snapshots, err = s.Snapshots(ctx, time.Now()); err != nil {
		return r, err
	}
	return r, nil
}

// Render writes the report through the terminal output components (docs/design.md, Terminal output). Every section
// is shown, so it is seen to have been checked: an empty one is one line ending in "none", except Roots and Register
// snapshots, which say "none yet" with what to do, and Duplicates, which says "no groups" when it still says what is
// not counted on purpose.
func (r Report) Render(v *view.Renderer) {
	var roots []string
	var files, bytes int64
	for _, t := range r.Roots {
		roots = append(roots, t.Root.Path)
		files += t.Files
		bytes += t.Bytes
	}
	v.Heading("Register", view.Where{Roots: roots}, Plural(files, "file", "files"), Size(bytes))

	v.Blank()
	if len(r.Roots) == 0 {
		v.Section("Roots", "none yet: add one with filo roots add DIR")
	} else {
		v.Section("Roots")
	}
	RootEntries(v, r.Roots)

	v.Blank()
	r.renderRetention(v)

	v.Blank()
	if len(r.Largest) == 0 {
		v.Section("Largest files", "none")
	} else {
		v.Section("Largest files", "top "+Count(int64(len(r.Largest))))
		rows := make([]view.FileRow, len(r.Largest))
		for i, f := range r.Largest {
			rows[i] = view.FileRow{Root: f.Root, Rel: f.Rel, Cells: []string{Size(f.Size)}}
			if f.Links > 0 {
				rows[i].Note = "and " + Plural(int64(f.Links), "hard link", "hard links") + ", the same file"
			}
		}
		v.Files(view.FileList{Columns: []view.Column{{Header: "size", Right: true}}, Rows: rows})
	}

	v.Blank()
	d := r.Duplicates
	switch {
	case d.Groups == 0 && d.NotCounted == (store.NotCounted{}):
		v.Section("Duplicates", "none")
	case d.Groups == 0:
		v.Section("Duplicates", "no groups")
	default:
		v.Section("Duplicates", Plural(d.Groups, "group", "groups"), Plural(d.Copies, "copy", "copies"),
			Size(d.Reclaimable)+" reclaimable")
		groups := make([]view.DupGroup, len(d.Top))
		for i, g := range d.Top {
			dg := view.DupGroup{Name: filepath.Base(g.Copies[0].Rel), Each: Size(g.Size),
				Facts: []string{Plural(int64(len(g.Copies)), "copy", "copies"), Size(g.Reclaimable) + " reclaimable"}}
			for _, c := range g.Copies {
				dg.Copies = append(dg.Copies, view.Copy{Root: c.Root, Rel: c.Rel, Links: c.Links})
			}
			groups[i] = dg
		}
		v.Dups(groups, 0)
		if more := d.Groups - int64(len(d.Top)); more > 0 {
			v.More(int(more), "group", "groups")
		}
	}
	if line := NotCountedLine(d.NotCounted); line != "" {
		v.Count(view.Kept, line)
	}

	v.Blank()
	if r.BrokenTotal == 0 {
		v.Section("Broken symlinks", "none")
	} else {
		v.Section("Broken symlinks", Count(r.BrokenTotal))
		rows := make([]view.FileRow, len(r.Broken))
		for i, l := range r.Broken {
			rows[i] = view.FileRow{Root: l.Root, Rel: l.Rel, Note: "-> " + l.Target}
		}
		v.Files(view.FileList{Rows: rows, ByRoot: true})
		if more := r.BrokenTotal - int64(len(r.Broken)); more > 0 {
			v.More(int(more), "symlink", "symlinks")
		}
	}

	v.Blank()
	if r.UnreadTotal == 0 {
		v.Section("Unreadable files", "none")
	} else {
		v.Section("Unreadable files", Count(r.UnreadTotal))
		rows := make([]view.FileRow, len(r.Unreadable))
		for i, u := range r.Unreadable {
			rows[i] = view.FileRow{Root: u.Root, Rel: u.Rel, Cells: []string{fmt.Sprintf("scan %d", u.Since)}}
		}
		v.Files(view.FileList{Columns: []view.Column{{Header: "since"}}, Rows: rows})
		if more := r.UnreadTotal - int64(len(r.Unreadable)); more > 0 {
			v.More(int(more), "file", "files")
		}
	}

	v.Blank()
	if len(r.Events) == 0 {
		v.Section("Recent events", "none")
	} else {
		v.Section("Recent events", fmt.Sprintf("latest %s", Count(int64(len(r.Events)))))
		rows := make([]view.FileRow, len(r.Events))
		for i, e := range r.Events {
			rows[i] = EventRow(e.RootPath, e.Event, fmt.Sprintf("scan %d", e.ScanID))
		}
		v.Files(view.FileList{Columns: []view.Column{{Header: "scan"}, {Header: "event"}}, Rows: rows})
	}

	v.Blank()
	r.renderSnapshots(v)
}

// RootEntries writes each root with its totals, its settings, the filesystem it is mounted on and its last scan:
// the report's Roots section and filo roots (docs/design.md, Terminal output, Scan and roots).
func RootEntries(v *view.Renderer, roots []store.RootTotal) {
	for _, t := range roots {
		facts := []string{Plural(t.Files, "file", "files"), Size(t.Bytes)}
		if t.Links > 0 {
			facts = append(facts, Plural(t.Links, "symlink", "symlinks"))
		}
		v.Entry(2, t.Root.Path, facts...)
		v.Detail(4, v.Join(SettingsParts(t.Root.Settings)...))
		if t.Root.MountPath != "" {
			v.DetailPath(4, "on the filesystem mounted at ", t.Root.MountPath)
		}
		if ls := t.LastScan; ls != nil {
			scope := "whole root"
			if ls.Scope != "" {
				scope = ls.Scope
			}
			v.Detail(4, fmt.Sprintf("last scan %d, %s, %s: %s seen, new %s, changed %s, moved %s, missing %s, errors %s",
				ls.ID, Time(ls.FinishedUTC), scope, Count(ls.FilesSeen), Count(ls.New), Count(ls.Changed),
				Count(ls.Moved), Count(ls.Missing), Count(ls.Errors)))
		} else {
			v.Detail(4, "never scanned")
		}
	}
}

// SettingsParts describes a root's settings in a few words each: whether hidden files are skipped, and the names
// ignored. The names always skipped (node_modules and the rest) are left out: filo help roots lists them.
func SettingsParts(set store.Settings) []string {
	parts := []string{"hidden files skipped"}
	if set.IncludeHidden {
		parts[0] = "hidden files included"
	}
	if len(set.Ignore) > 0 {
		parts = append(parts, "ignoring "+strings.Join(set.Ignore, ", "))
	}
	return parts
}

// NotCountedLine says what is kept out of the duplicate groups on purpose: "not counted: 1 excluded directory, 2
// contents always, 1 set of records (filo duplicates)", or "" when nothing is.
func NotCountedLine(n store.NotCounted) string {
	var parts []string
	if n.Dirs > 0 {
		parts = append(parts, Plural(n.Dirs, "excluded directory", "excluded directories"))
	}
	if n.Always > 0 {
		parts = append(parts, Plural(n.Always, "content always", "contents always"))
	}
	if n.RecordSets > 0 {
		parts = append(parts, Plural(n.RecordSets, "set of records", "sets of records"))
	}
	if len(parts) == 0 {
		return ""
	}
	return "not counted: " + strings.Join(parts, ", ") + " (filo duplicates)"
}

// renderRetention writes the retention section: how many files need a retention (and where most of them are), how
// many are kept, past their retention, or deleted by filo.
func (r Report) renderRetention(v *view.Renderer) {
	var total int64
	for _, n := range r.Status {
		total += n
	}
	if total == 0 {
		v.Section("Retention", "none")
		return
	}
	v.Section("Retention", Plural(total, "file or symlink", "files and symlinks"))
	if r.Undecided == 0 {
		v.Count(view.Done, "no file needs a retention")
	} else {
		var parts []string
		for _, st := range store.Statuses {
			if n := r.Status[st]; n > 0 && st.NeedsDecision() {
				parts = append(parts, StatusLabel(st)+" "+Count(n))
			}
		}
		v.Count(view.Attention, Plural(r.Undecided, "needs", "need")+" a retention ("+
			strings.Join(parts, ", ")+")")
	}
	if r.Undecided > 0 { // right under the line it explains
		v.Detail(4, fmt.Sprintf("Where most need one (to depth %d)", DirDepth))
		rows := make([]view.FileRow, len(r.UndecidedIn))
		for i, g := range r.UndecidedIn {
			rows[i] = view.FileRow{Root: g.Root, Rel: g.Rel, Cells: []string{Count(g.Files), Size(g.Bytes)}}
		}
		v.Files(view.FileList{Columns: []view.Column{{Header: "files", Right: true}, {Header: "size", Right: true}},
			Rows: rows})
		v.Detail(4, "Next: filo export retention [DIR] -o FILE, then filo update FILE")
	}

	if n := r.Status[store.StatusKept]; n > 0 {
		v.Count(view.Neutral, Count(n)+" kept until a later date")
	}
	if n := r.Status[store.StatusForever]; n > 0 {
		v.Count(view.Neutral, Count(n)+" kept forever")
	}
	if n := r.Status[store.StatusExpired]; n > 0 {
		v.Count(view.Attention, Plural(n, "past its", "past their")+" retention: see filo delete")
	}
	if d := r.Deleted; d.Files > 0 {
		last := ""
		if d.LastRunUTC != "" {
			last = fmt.Sprintf(" (last run %s: %s, %s)", Time(d.LastRunUTC), Plural(d.LastFiles, "file", "files"),
				Size(d.LastBytes))
		}
		v.Count(view.Neutral, fmt.Sprintf("deleted by filo: %s, %s freed%s", Plural(d.Files, "file", "files"),
			Size(d.Bytes), last))
	}
}

// renderSnapshots shows the register's copies, and warns when they are missing or behind, which is how a failing
// snapshot gets noticed when filo runs from a timer.
func (r Report) renderSnapshots(v *view.Renderer) {
	sn := r.Snapshots
	upgrades := ""
	if sn.PreUpgrade > 0 {
		upgrades = fmt.Sprintf(" (and %s from before an upgrade)", Plural(int64(sn.PreUpgrade), "copy", "copies"))
	}
	switch {
	case sn.Problem != "":
		v.Section("Register snapshots")
		v.CountName(view.Attention, "cannot read ", sn.Dir, ": "+sn.Problem)
	case sn.Count == 0 && sn.LatestWrite.IsZero():
		v.Section("Register snapshots", "none yet")
		v.Detail(4, "one is written after each command that changes the register"+upgrades)
	case sn.Count == 0:
		v.Section("Register snapshots", "none")
		v.CountName(view.Attention, "none in ", sn.Dir, ", though the register has changed"+upgrades+
			"; see the warning after each command")
	default:
		v.Section("Register snapshots", fmt.Sprintf("%d, latest %s%s", sn.Count, LocalTime(sn.Latest), upgrades))
		v.DetailPath(4, "", sn.Dir)
		if sn.Latest.Before(sn.LatestWrite) {
			v.Count(view.Attention, "the register changed at "+LocalTime(sn.LatestWrite)+", after the latest "+
				"snapshot; see the warning after each command")
		}
	}
	if sn.Future > 0 {
		v.Count(view.Attention, Plural(int64(sn.Future), "snapshot is", "snapshots are")+" dated in the future (was the "+
			"clock ahead?); kept, but not counted as the latest")
	}
}

// DirGroup is a directory and the files counted in it.
type DirGroup struct {
	Root, Rel    string // the root (absolute) and the directory relative to it ("." for the root itself)
	Files, Bytes int64
}

// AbsDir is the directory's absolute path.
func (g DirGroup) AbsDir() string { return filepath.Join(g.Root, filepath.FromSlash(g.Rel)) }

// DirGroups counts files by their directory, cut at depth levels below the root, largest count first, at most top.
// A symlink counts as a file but adds no bytes (its size is only the length of its target).
func DirGroups(items []store.FileItem, depth, top int) []DirGroup {
	m := map[string]*DirGroup{}
	for _, it := range items {
		parts := strings.Split(it.Path, "/")
		dirs := parts[:len(parts)-1]
		if len(dirs) > depth {
			dirs = dirs[:depth]
		}
		dir := filepath.Join(append([]string{it.RootPath}, dirs...)...)
		g := m[dir]
		if g == nil {
			rel := strings.Join(dirs, "/")
			if rel == "" {
				rel = "."
			}
			g = &DirGroup{Root: it.RootPath, Rel: rel}
			m[dir] = g
		}
		g.Files++
		if it.Kind == "file" {
			g.Bytes += it.Size
		}
	}
	out := make([]DirGroup, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b DirGroup) int {
		if a.Files != b.Files {
			if a.Files > b.Files {
				return -1
			}
			return 1
		}
		if a.Root != b.Root {
			return strings.Compare(a.Root, b.Root)
		}
		return strings.Compare(a.Rel, b.Rel)
	})
	if len(out) > top {
		out = out[:top]
	}
	return out
}

// EventParts splits an event into its kind as a word ("moved", "missing", …) and, for a move, the path it came
// from ("" otherwise); its path and detail are the event's own.
func EventParts(e store.Event) (word, from string) {
	switch e.Kind {
	case "moved":
		return "moved", e.OtherPath
	case "possible_move":
		return "possible move", e.OtherPath
	case store.EventDeleted:
		return "deleted", ""
	case store.EventSameAsDeleted:
		return "new", ""
	case store.EventOtherFS:
		return "other filesystem", ""
	}
	return e.Kind, ""
}

// EventRow is one event as a row of an event list, under root: the cells given, then the event's word, its path
// (with the path it came from for a move) and its detail as the note. The report's recent events and a scan's
// changes are both made of these rows, so an event reads the same in both.
func EventRow(root string, e store.Event, cells ...string) view.FileRow {
	word, from := EventParts(e)
	rel := e.Path
	if rel == "" {
		rel = "." // an event on the root itself (an unreadable root folder)
	}
	fromRoot := ""
	if _, other, ok := strings.Cut(e.Detail, store.EventFromRoot); ok && from != "" {
		fromRoot = other // came back from another root: its earlier path is relative to that root
	}
	return view.FileRow{Root: root, Rel: rel, From: from, FromRoot: fromRoot, Note: e.Detail,
		Cells: append(slices.Clip(cells), word)}
}
