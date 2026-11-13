// Package report builds the `filo report` overview from the register and formats it as text.
package report

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/store"
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

// WriteText prints the report.
func (r Report) WriteText(w io.Writer) error {
	p := &printer{w: w}
	p.line("Roots")
	if len(r.Roots) == 0 {
		p.line("  none yet; add one with: filo roots add DIR")
	}
	var files, bytes int64
	for _, t := range r.Roots {
		files += t.Files
		bytes += t.Bytes
		p.line("  %s: %s, %s, %s", t.Root.Path, Plural(t.Files, "file", "files"), Size(t.Bytes),
			Plural(t.Links, "symlink", "symlinks"))
		if t.Root.MountPath != "" {
			p.line("    on the filesystem mounted at %s", t.Root.MountPath)
		}
		if ls := t.LastScan; ls != nil {
			scope := "whole root"
			if ls.Scope != "" {
				scope = ls.Scope
			}
			p.line("    last scan %d at %s (%s): %s seen; new %s, changed %s, moved %s, missing %s, errors %s",
				ls.ID, Time(ls.FinishedUTC), scope, Count(ls.FilesSeen), Count(ls.New), Count(ls.Changed), Count(ls.Moved),
				Count(ls.Missing), Count(ls.Errors))
		} else {
			p.line("    never scanned")
		}
	}
	if len(r.Roots) > 1 {
		p.line("  total: %s, %s", Plural(files, "file", "files"), Size(bytes))
	}

	p.blank()
	if r.Undecided == 0 {
		p.line("Needing a retention: none")
	} else {
		var parts []string
		for _, st := range store.Statuses {
			if n := r.Status[st]; n > 0 && st.NeedsDecision() {
				parts = append(parts, StatusLabel(st)+" "+Count(n))
			}
		}
		p.line("Needing a retention: %s (%s)", Plural(r.Undecided, "file", "files"), strings.Join(parts, ", "))
		p.line("  directories with the most (to depth %d):", DirDepth)
		for _, g := range r.UndecidedIn {
			p.line("  %8s  %10s  %s", Count(g.Files), Size(g.Bytes), g.Dir)
		}
		p.line("  set retentions with: filo export retention [DIR] -o FILE, then filo update FILE")
	}
	if n := r.Status[store.StatusKept]; n > 0 {
		p.line("Kept until a later date: %s", Plural(n, "file", "files"))
	}
	if n := r.Status[store.StatusForever]; n > 0 {
		p.line("Kept forever: %s", Plural(n, "file", "files"))
	}
	if n := r.Status[store.StatusExpired]; n > 0 {
		p.line("Past their retention: %s; see filo delete", Plural(n, "file", "files"))
	}
	if d := r.Deleted; d.Files > 0 {
		last := ""
		if d.LastRunUTC != "" {
			last = fmt.Sprintf(" (last run %s: %s, %s)", Time(d.LastRunUTC), Plural(d.LastFiles, "file", "files"),
				Size(d.LastBytes))
		}
		p.line("Deleted by filo: %s, %s freed%s", Plural(d.Files, "file", "files"), Size(d.Bytes), last)
	}

	p.blank()
	p.line("Largest files")
	if len(r.Largest) == 0 {
		p.line("  none")
	}
	for _, f := range r.Largest {
		p.line("  %10s  %s", Size(f.Size), f.Path)
	}

	p.blank()
	d := r.Duplicates
	if d.Groups == 0 {
		p.line("Duplicates: none")
	} else {
		p.line("Duplicates: %s, %s, %s reclaimable", Plural(d.Groups, "group", "groups"), Plural(d.Copies, "file", "files"),
			Size(d.Reclaimable))
		for _, g := range d.Top {
			p.line("  %d copies of %s, %s reclaimable", len(g.Paths), Size(g.Size), Size(g.Reclaimable))
			for _, path := range g.Paths {
				p.line("    %s", path)
			}
		}
		if int64(len(d.Top)) < d.Groups {
			p.line("  and %s more", Plural(d.Groups-int64(len(d.Top)), "group", "groups"))
		}
	}
	if d.Separate > 0 {
		p.line("  %s marked separate (%s): identical, but not counted as duplicates",
			Plural(d.Separate, "content", "contents"), Plural(d.SeparateFiles, "file", "files"))
	}
	if d.Marked > 0 {
		p.line("  %s marked for deletion; see filo delete", Plural(d.Marked, "copy", "copies"))
	}

	p.blank()
	if r.BrokenTotal == 0 {
		p.line("Broken symlinks: none")
	} else {
		p.line("Broken symlinks: %s", Count(r.BrokenTotal))
		for _, l := range r.Broken {
			p.line("  %s -> %s", l.Path, l.Target)
		}
		if int64(len(r.Broken)) < r.BrokenTotal {
			p.line("  and %s more", Count(r.BrokenTotal-int64(len(r.Broken))))
		}
	}

	p.blank()
	if r.UnreadTotal == 0 {
		p.line("Unreadable files: none")
	} else {
		p.line("Unreadable files: %s", Count(r.UnreadTotal))
		for _, u := range r.Unreadable {
			p.line("  since scan %d  %s", u.Since, u.Path)
		}
		if int64(len(r.Unreadable)) < r.UnreadTotal {
			p.line("  and %s more", Count(r.UnreadTotal-int64(len(r.Unreadable))))
		}
	}

	p.blank()
	p.line("Recent events")
	if len(r.Events) == 0 {
		p.line("  none")
	}
	for _, e := range r.Events {
		where := ""
		if len(r.Roots) > 1 {
			where = "  in " + e.RootPath
		}
		p.line("  scan %d  %s%s", e.ScanID, EventText(e.Event), where)
	}

	p.blank()
	r.writeSnapshots(p)
	return p.err
}

// writeSnapshots shows the register's copies, and warns when they are missing or behind, which is how a failing
// snapshot gets noticed when filo runs from a timer.
func (r Report) writeSnapshots(p *printer) {
	sn := r.Snapshots
	p.line("Register snapshots")
	upgrades := ""
	if sn.PreUpgrade > 0 {
		upgrades = fmt.Sprintf(" (and %s from before an upgrade)", Plural(int64(sn.PreUpgrade), "copy", "copies"))
	}
	switch {
	case sn.Problem != "":
		p.line("  warning: cannot read %s: %s", sn.Dir, sn.Problem)
	case sn.Count == 0 && sn.LatestWrite.IsZero():
		p.line("  none yet; one is written after each command that changes the register%s", upgrades)
	case sn.Count == 0:
		p.line("  warning: none in %s, though the register has changed%s; see the warning after each command",
			sn.Dir, upgrades)
	default:
		p.line("  %d in %s, latest %s%s", sn.Count, sn.Dir, LocalTime(sn.Latest), upgrades)
		if sn.Latest.Before(sn.LatestWrite) {
			p.line("  warning: the register changed at %s, after the latest snapshot; see the warning after each "+
				"command", LocalTime(sn.LatestWrite))
		}
	}
	if sn.Future > 0 {
		p.line("  warning: %s dated in the future (was the clock ahead?); kept, but not counted as the latest",
			Plural(int64(sn.Future), "snapshot is", "snapshots are"))
	}
}

// DirGroup is a directory and the files counted in it.
type DirGroup struct {
	Dir          string
	Files, Bytes int64
}

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
			g = &DirGroup{Dir: dir}
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
		return strings.Compare(a.Dir, b.Dir)
	})
	if len(out) > top {
		out = out[:top]
	}
	return out
}

// EventText renders one event on a line, without its root.
func EventText(e store.Event) string {
	var b strings.Builder
	switch e.Kind {
	case "moved":
		fmt.Fprintf(&b, "moved     %s -> %s", e.OtherPath, e.Path)
	case "possible_move":
		fmt.Fprintf(&b, "possible  %s -> %s ?", e.OtherPath, e.Path)
	case "missing":
		fmt.Fprintf(&b, "missing   %s", e.Path)
	case "changed":
		fmt.Fprintf(&b, "changed   %s", e.Path)
	case "error":
		fmt.Fprintf(&b, "error     %s", e.Path)
	case store.EventDeleted:
		fmt.Fprintf(&b, "deleted   %s", e.Path)
	case store.EventSameAsDeleted:
		fmt.Fprintf(&b, "new       %s", e.Path)
	default:
		fmt.Fprintf(&b, "%-9s %s", e.Kind, e.Path)
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, " (%s)", e.Detail)
	}
	return b.String()
}

type printer struct {
	w   io.Writer
	err error
}

func (p *printer) line(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format+"\n", args...)
	}
}

func (p *printer) blank() { p.line("") }
