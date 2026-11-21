// Package show is filo show (docs/design.md, Showing and setting a file's details): everything Filo holds about a
// file, read from the register, with a quick look at the disk (an lstat, no content read) and, with --verify, a
// re-hash. It never changes anything: neither the files nor the register.
package show

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"strconv"
	"time"

	"github.com/alansav/filo/internal/fsid"
	"github.com/alansav/filo/internal/hasher"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
	"github.com/alansav/filo/internal/walk"
)

// Disk is what the quick look at the disk found at the file's path.
type Disk int

const (
	// DiskUnchanged: as the register has it (size, modified time, inode, change time; a symlink's target).
	DiskUnchanged Disk = iota
	// DiskChanged: something else is there: changed since the last scan.
	DiskChanged
	// DiskGone: nothing is there.
	DiskGone
	// DiskThere: a file not present in the register (missing, or deleted by filo) has something at its path again.
	DiskThere
	// DiskNoRoot: the root cannot be reached (unmounted, or no longer a directory), so the path says nothing.
	DiskNoRoot
	// DiskError: the path could not be checked.
	DiskError
)

// Verify is what re-reading the file found (--verify).
type Verify int

const (
	VerifyNotAsked Verify = iota
	VerifyMatches         // the content is the one the register holds
	VerifyDiffers         // it is not
	VerifyFailed          // it could not be read
	VerifySkipped         // nothing to read: not a present regular file with a hash
)

// File is one file as filo show shows it.
type File struct {
	store.Shown
	Disk      Disk
	DiskWhy   string // DiskNoRoot, DiskError: why
	Verify    Verify
	VerifyWhy string // VerifyFailed: why
}

// Look checks the file on disk: an lstat compared with the register, as a scan compares (docs/design.md, Scan and
// reconcile, step 3), and, when verify is set, a re-hash of a present regular file. root is the file's root, checked
// first, so an unmounted disk never reads as a file gone.
func Look(ctx context.Context, sh store.Shown, root store.Root, verify bool) File {
	f := File{Shown: sh}
	if err := store.CheckRoot(root); err != nil {
		f.Disk, f.DiskWhy = DiskNoRoot, err.Error()
		if verify {
			f.Verify, f.VerifyWhy = VerifyFailed, "its root cannot be reached"
		}
		return f
	}
	abs := sh.AbsPath()
	fi, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		f.Disk = DiskGone
		if !sh.Present {
			f.Disk = DiskUnchanged // gone, as the register has it
		}
	case err != nil:
		f.Disk, f.DiskWhy = DiskError, err.Error()
	case !sh.Present:
		f.Disk = DiskThere
	case !same(sh, fi, abs):
		f.Disk = DiskChanged
	}
	if !verify {
		return f
	}
	if !sh.Present || sh.Kind != "file" || sh.Hash == "" || f.Disk == DiskGone {
		f.Verify = VerifySkipped
		return f
	}
	res, err := hasher.File(ctx, abs)
	switch {
	case err != nil:
		f.Verify, f.VerifyWhy = VerifyFailed, err.Error()
	case string(res.Sum) == sh.Hash:
		f.Verify = VerifyMatches
	default:
		f.Verify = VerifyDiffers
	}
	return f
}

// same reports whether what lstat found is the file the register holds, as a scan compares: a regular file with the
// same size and modified time, and the same inode and change time where both are known; a symlink with the same
// target and broken state (its change time is not compared, as a scan does not).
func same(sh store.Shown, fi fs.FileInfo, abs string) bool {
	if sh.Kind == "symlink" {
		if fi.Mode()&fs.ModeSymlink == 0 {
			return false
		}
		target, err := os.Readlink(abs)
		if err != nil || target != sh.LinkTarget {
			return false
		}
		broken, err := walk.Broken(abs)
		return err != nil || broken == sh.LinkBroken // cannot tell: as a scan, no change claimed
	}
	if !fi.Mode().IsRegular() || fi.Size() != sh.Size || fi.ModTime().UnixNano() != sh.MtimeNS {
		return false
	}
	if id, ok := fsid.FromFileInfo(fi); ok && id.Ino != 0 && sh.Ino != 0 && id.Ino != sh.Ino {
		return false
	}
	ctime := fsid.ChangeTimeNS(fi)
	return ctime == 0 || sh.CtimeNS == 0 || ctime == sh.CtimeNS
}

// date shows a stored UTC timestamp as a local date; anything unparsable as stored.
func date(utc string) string {
	t, err := time.Parse(time.RFC3339Nano, utc)
	if err != nil {
		return utc
	}
	return t.Local().Format("2006-01-02")
}

// TimeLeft says how far a date (YYYY-MM-DD) is from today: "in 2 years 2 months", "in 12 days", "today", or, once
// past, "1 year 3 months ago". Years and months while there is at least a month, days under a month.
func TimeLeft(today, until string) string {
	t0, err0 := time.Parse(time.DateOnly, today)
	t1, err1 := time.Parse(time.DateOnly, until)
	if err0 != nil || err1 != nil {
		return ""
	}
	if t0.Equal(t1) {
		return "today"
	}
	past := t1.Before(t0)
	if past {
		t0, t1 = t1, t0
	}
	months := (t1.Year()-t0.Year())*12 + int(t1.Month()) - int(t0.Month())
	if t1.Day() < t0.Day() {
		months--
	}
	var span string
	if months >= 1 {
		y, m := months/12, months%12
		switch {
		case m == 0:
			span = report.Plural(int64(y), "year", "years")
		case y == 0:
			span = report.Plural(int64(m), "month", "months")
		default:
			span = report.Plural(int64(y), "year", "years") + " " + report.Plural(int64(m), "month", "months")
		}
	} else {
		span = report.Plural(int64(t1.Sub(t0).Hours()/24), "day", "days")
	}
	if past {
		return span + " ago"
	}
	return "in " + span
}

// status is the title's summary of where the file stands, and whether it needs a decision.
func (f File) status() (string, bool) {
	switch {
	case f.DeletedUTC != "":
		return "deleted by filo on " + date(f.DeletedUTC), false
	case !f.Present:
		return "missing since " + date(f.GoneUTC) + " (scan " + strconv.FormatInt(f.GoneScan, 10) + ")", true
	}
	switch f.Status {
	case store.StatusKept:
		return "kept until " + f.KeepUntil, false
	case store.StatusForever:
		return "kept forever", false
	case store.StatusExpired:
		return "expired " + f.KeepUntil, true
	case store.StatusChanged:
		return "changed since its retention was set", true
	case store.StatusUndated:
		return "needs a retention", true
	case store.StatusNeedsFileDate:
		return "needs a file_date", true
	case store.StatusPossibleMove:
		return "possible move", true
	case store.StatusUnreadable:
		return "unreadable", true
	}
	return string(f.Status), false
}

// Render writes the file: its title line (with [n/total] when total > 1), then its File, Retention and Copies parts,
// and with history every path it has had. today (YYYY-MM-DD) is for the time left.
func (f File) Render(v *view.Renderer, n, total int, today string, history bool) {
	st, attention := f.status()
	dir := path.Dir(f.Path)
	if dir == "." {
		dir = ""
	}
	v.Subject(view.Subject{N: n, Total: total, Name: path.Base(f.Path), Status: st, Attention: attention,
		Facts: []string{report.Size(f.Size)}, Dir: store.AbsPath(f.RootPath, dir)})
	file, retention := f.fileFields(history), f.retentionFields(today)
	w := view.LabelWidth(file, retention)
	v.Part("File")
	v.Fields(w, file)
	v.Part("Retention")
	v.Fields(w, retention)
	f.renderCopies(v)
	if history {
		v.Part("Paths", view.Plural(int64(len(f.Paths)), "path", "paths")+", newest first")
		rows := make([]view.FileRow, len(f.Paths))
		for i, p := range f.Paths {
			rows[i] = view.FileRow{Root: p.RootPath, Rel: p.Path, Note: seen(p)}
		}
		v.Files(view.FileList{Rows: rows})
	}
}

// seen is when Filo saw a file at a path: "2026-11-12 to 2026-11-13", or "2026-11-13 to now".
func seen(p store.ShownPath) string {
	to := "now"
	if p.ToScan != 0 {
		to = date(p.ToUTC)
	}
	return date(p.FromUTC) + " to " + to
}

func (f File) fileFields(history bool) []view.Field {
	pathField := view.Field{Label: "path", Value: f.AbsPath(), Path: true}
	switch f.Disk {
	case DiskGone:
		pathField.Attention = "not on disk"
	case DiskThere:
		pathField.Attention = "something is at this path now: run filo scan"
	case DiskNoRoot:
		pathField.Attention = "cannot be checked: " + f.DiskWhy
	case DiskError:
		pathField.Attention = "cannot be checked: " + f.DiskWhy
	}
	fields := []view.Field{pathField,
		{Label: "root", Value: f.RootPath, Path: true},
		{Label: "size", Value: report.Size(f.Size)},
	}
	if f.Kind == "symlink" {
		link := view.Field{Label: "link to", Value: f.LinkTarget}
		if f.LinkBroken {
			link.Attention = "broken"
		}
		fields = append(fields, link)
	}
	modified := view.Field{Label: "modified", Value: report.LocalTime(time.Unix(0, f.MtimeNS))}
	if f.Present {
		switch f.Disk {
		case DiskUnchanged:
			modified.Note = "unchanged since the last scan"
			if f.LastScanUTC != "" {
				modified.Note += " (" + date(f.LastScanUTC) + ")"
			}
			if f.Kind == "file" && (f.Hash == "" || f.UnreadableSince != 0) {
				modified.Note += "; the next scan reads it again" // as a scan would: no content on record to trust
			}
		case DiskChanged:
			modified.Attention = "changed on disk since the last scan: run filo scan"
		}
	}
	fields = append(fields, modified)
	if f.Kind == "file" {
		sum := view.Field{Label: "sha256", Value: f.Hash}
		if f.Hash == "" {
			sum.Value = "none: never read"
		}
		switch f.Verify {
		case VerifyMatches:
			sum.Note = "content matches the register (read now)"
		case VerifyDiffers:
			sum.Attention = "differs from the register (read now)"
		case VerifyFailed:
			sum.Attention = "could not be read: " + f.VerifyWhy
		case VerifySkipped:
			sum.Note = "not read: no content to compare"
		}
		fields = append(fields, sum)
	}
	fields = append(fields, view.Field{Label: "first seen",
		Value: date(f.FirstSeenUTC) + " (scan " + strconv.FormatInt(f.FirstSeenScan, 10) + ")"})
	if len(f.Paths) > 1 && !history {
		p := f.Paths[1]
		moved := view.Field{Label: "moved from", Value: p.AbsPath(), Path: true, Note: seen(p)}
		if more := len(f.Paths) - 2; more > 0 {
			moved.Note += "   ... " + strconv.Itoa(more) + " more: --history"
		}
		fields = append(fields, moved)
	}
	return fields
}

func (f File) retentionFields(today string) []view.Field {
	var status view.Field
	status.Label = "status"
	switch {
	case f.DeletedUTC != "":
		status.Value = "deleted by filo"
	case !f.Present:
		status.Attention = "missing: run filo scan, if it was moved"
	default:
		switch f.Status {
		case store.StatusKept:
			status.Value = "kept"
		case store.StatusForever:
			status.Value = "forever"
		case store.StatusExpired:
			status.Attention = "expired: a deletion candidate (filo delete)"
		case store.StatusChanged:
			status.Attention = "changed: past its retention, with other content than it was set on"
		case store.StatusUndated:
			status.Attention = "undated: no retention"
		case store.StatusNeedsFileDate:
			status.Attention = "needs a file_date: its policy " + f.Policy + " (" + f.PolicyValue + ") counts from one"
		case store.StatusPossibleMove:
			status.Attention = "possible move: it may be " + f.MoveFrom
		case store.StatusUnreadable:
			status.Attention = "unreadable since " + date(f.UnreadableSinceUTC)
		}
	}
	fields := []view.Field{status}

	retention := view.Field{Label: "retention", Value: f.Retention}
	switch {
	case f.Policy != "":
		value := f.PolicyValue
		if value == "" {
			value = "no value yet"
		}
		retention.Value = f.Policy + " (" + value + ")"
		if f.RuleID != 0 {
			retention.Value += ", a policy set by rule " + path.Join(f.RuleDir, f.RulePattern)
		}
	case f.Retention == "":
		retention.Value = "none"
	}
	fields = append(fields, retention)
	if f.FileDate != "" {
		fields = append(fields, view.Field{Label: "file_date", Value: f.FileDate})
	}
	if f.KeepUntil != "" {
		keep := view.Field{Label: "keep_until", Value: f.KeepUntil}
		if f.Present && f.KeepUntil != store.Forever {
			if left := TimeLeft(today, f.KeepUntil); f.KeepUntil < today {
				keep.Attention = left
			} else {
				keep.Note = left
			}
		}
		fields = append(fields, keep)
	}
	if f.ReviewedUTC != "" {
		by := date(f.ReviewedUTC)
		if f.ReviewedBy != "" {
			by += ", by filo " + f.ReviewedBy
		}
		if f.Kind == "file" && f.ReviewedHash != "" && f.Hash != "" {
			if f.ReviewedHash == f.Hash {
				by += ", on this content"
			} else {
				by += ", on other content"
			}
		}
		fields = append(fields, view.Field{Label: "reviewed", Value: by})
	}
	return fields
}

func (f File) renderCopies(v *view.Renderer) {
	if len(f.Copies) == 0 {
		return
	}
	facts := []string{report.Plural(int64(len(f.Copies)), "other file", "other files") + " with the same content"}
	switch {
	case !f.Present:
	case f.Excluded:
		facts = append(facts, "not counted: this file is under an excluded directory")
	case f.Always:
		facts = append(facts, "marked always: never a duplicate")
	case f.OneRecordSet:
		facts = append(facts, "kept as separate records")
	case f.CopiesInGroups:
		facts = append(facts, "counted as a duplicate")
	}
	v.Part("Copies", facts...)
	rows := make([]view.FileRow, len(f.Copies))
	for i, c := range f.Copies {
		note := "until " + c.KeepUntil
		switch c.KeepUntil {
		case store.Forever:
			note = "kept forever"
		case "":
			note = "no retention"
		}
		rows[i] = view.FileRow{Root: c.RootPath, Rel: c.Path, Note: note}
	}
	v.Files(view.FileList{Rows: rows})
}
