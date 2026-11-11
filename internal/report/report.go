// Package report builds the `filo report` overview from the register and formats it as text.
package report

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/alansav/filo/internal/store"
)

// Report is everything `filo report` shows.
type Report struct {
	Roots       []store.RootTotal
	Largest     []store.FileRow
	Duplicates  store.Duplicates
	Broken      []store.LinkRow
	BrokenTotal int64
	Events      []store.EventRow
}

// Build reads the report from the register, with top entries in each list.
func Build(ctx context.Context, s *store.Store, top int) (Report, error) {
	var r Report
	var err error
	if r.Roots, err = s.RootTotals(ctx); err != nil {
		return r, err
	}
	if r.Largest, err = s.Largest(ctx, top); err != nil {
		return r, err
	}
	if r.Duplicates, err = s.Duplicates(ctx, top); err != nil {
		return r, err
	}
	if r.Broken, r.BrokenTotal, err = s.BrokenLinks(ctx, top); err != nil {
		return r, err
	}
	if r.Events, err = s.RecentEvents(ctx, top); err != nil {
		return r, err
	}
	return r, nil
}

// WriteText prints the report.
func (r Report) WriteText(w io.Writer) error {
	p := &printer{w: w}
	p.line("Roots")
	if len(r.Roots) == 0 {
		p.line("  none yet; add one with: filo roots add PATH")
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
	return p.err
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
