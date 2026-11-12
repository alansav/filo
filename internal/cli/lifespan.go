package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/lifespan"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
)

// now is the clock for keep_until dates; tests replace it.
var now = time.Now

func today() string { return lifespan.Today(now().Local()) }

// exportKinds are the CSVs filo can export. Each names what the CSV is for.
var exportKinds = []string{"lifespan"}

// filterFor maps an optional DIR to a filter, and says where it points.
func filterFor(ctx context.Context, s *store.Store, pos []string) (store.Filter, string, error) {
	if len(pos) == 0 {
		return store.Filter{}, "", nil
	}
	root, scope, err := s.Resolve(ctx, pos[0])
	if err != nil {
		return store.Filter{}, "", err
	}
	where := root.Path
	if scope != "" {
		where = filepath.Join(root.Path, filepath.FromSlash(scope))
	}
	return store.Filter{RootID: root.ID, Scope: scope}, where, nil
}

func (e *env) export(ctx context.Context, dbBefore string, args []string) error {
	fl := e.flags("export")
	db := dbFlag(fl, dbBefore)
	out := fl.String("o", "", "")
	pos, err := e.parse(fl, "export", args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError{"export needs what to export: filo export lifespan [DIR] -o FILE"}
	}
	if !slices.Contains(exportKinds, pos[0]) {
		return usageError{fmt.Sprintf("unknown export %q; the one export is lifespan", pos[0])}
	}
	pos = pos[1:]
	if len(pos) > 1 {
		return usageError{"export lifespan takes at most one DIR"}
	}
	if *out == "" {
		return usageError{"export needs -o FILE: the CSV is written to a file, never to the terminal"}
	}
	if err := checkTildes("DIR", pos...); err != nil {
		return err
	}
	if err := checkTilde("-o", *out); err != nil {
		return err
	}
	path, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists; choose another name (filo never overwrites a file)", path)
	}

	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer s.Close()
	f, where, err := filterFor(ctx, s, pos)
	if err != nil {
		return err
	}
	items, err := s.Files(ctx, today(), f)
	if err != nil {
		return err
	}
	if where != "" {
		where = " under " + where
	}
	if len(items) == 0 {
		fmt.Fprintf(e.stdout, "No files%s; nothing exported.\n", where)
		return nil
	}

	// O_EXCL: an existing file is never overwritten, whatever it is.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists; choose another name (filo never overwrites a file)", path)
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	id, err := s.RecordExport(ctx, path, now(), items, func(id int64) error {
		bw := bufio.NewWriter(file)
		if err := lifespan.WriteCSV(bw, id, items, time.Local); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		if err := file.Sync(); err != nil {
			return err
		}
		return file.Close()
	})
	if err != nil {
		file.Close()
		os.Remove(path) // created by this command a moment ago, and incomplete
		return fmt.Errorf("export to %s: %w", path, err)
	}
	undated := 0
	for _, it := range items {
		if it.KeepUntil == "" {
			undated++
		}
	}
	fmt.Fprintf(e.stdout, "Exported %s%s to %s (export %d), %s without a keep_until.\n",
		report.Plural(int64(len(items)), "file", "files"), where, path, id, report.Count(int64(undated)))
	fmt.Fprintf(e.stdout, "Set keep_until (YYYY-MM-DD after today, or forever) where you want it, then run:\n"+
		"  filo update %s\n", path)
	return nil
}

// clearsShown is how many files the clear prompt names before "and N more".
const clearsShown = 10

func (e *env) update(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("update")
	db := dbFlag(fs, dbBefore)
	dry := fs.Bool("dry-run", false, "")
	pos, err := e.parse(fs, "update", args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{"update takes exactly one FILE"}
	}
	if err := checkTilde("FILE", pos[0]); err != nil {
		return err
	}
	path, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	parsed, err := lifespan.ReadCSV(file)
	file.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer s.Close()
	asked := false
	confirm := func(cs []store.Clear) bool {
		if !isTerminal(e.stdin) {
			return false
		}
		asked = true
		return e.confirmClears(cs)
	}
	res, err := s.UpdateLifespan(ctx, parsed.Rows, now(), today(), path, *dry, confirm)
	if err != nil {
		return err
	}

	type line struct {
		n   int
		msg string
	}
	var bad []line
	for _, p := range parsed.Problems {
		bad = append(bad, line{p.Line, p.Msg})
	}
	for _, r := range res.Refusals {
		where := r.Path
		if where == "" {
			where = fmt.Sprintf("file %d", r.FileID)
		}
		bad = append(bad, line{r.Line, where + ": " + r.Reason})
	}
	slices.SortStableFunc(bad, func(a, b line) int { return a.n - b.n })
	for _, b := range bad {
		fmt.Fprintf(e.stderr, "filo: %s line %d: %s\n", filepath.Base(path), b.n, b.msg)
	}

	w := e.stdout
	if *dry {
		fmt.Fprintln(w, "Dry run: nothing was written.")
		fmt.Fprintf(w, "Would set %s and clear %s.\n", report.Plural(int64(res.Set), "keep_until date", "keep_until dates"),
			report.Plural(int64(res.Cleared), "date", "dates"))
	} else {
		fmt.Fprintf(w, "Set %s, cleared %s.\n", report.Plural(int64(res.Set), "keep_until date", "keep_until dates"),
			report.Plural(int64(res.Cleared), "date", "dates"))
	}
	if res.ClearsSkipped > 0 {
		why := "not confirmed"
		if !asked {
			why = "there is no terminal to ask in; run filo update in a terminal to clear them"
		}
		fmt.Fprintf(w, "Left %s whose keep_until cell is blank (%s).\n",
			report.Plural(int64(res.ClearsSkipped), "date", "dates"), why)
	}
	var notes []string
	if res.Unchanged > 0 {
		notes = append(notes, report.Plural(int64(res.Unchanged), "row", "rows")+" unchanged")
	}
	if len(bad) > 0 {
		notes = append(notes, report.Plural(int64(len(bad)), "row", "rows")+" refused (listed above)")
	}
	if len(notes) > 0 {
		fmt.Fprintf(w, "%s.\n", strings.Join(notes, ", "))
	}
	if len(bad) > 0 {
		return errReported
	}
	return nil
}

// confirmClears lists the dates a blank cell would clear and asks once. Only "y" or "yes" confirms.
func (e *env) confirmClears(cs []store.Clear) bool {
	w := e.stdout
	fmt.Fprintf(w, "%s a keep_until date but a blank cell in the CSV:\n",
		report.Plural(int64(len(cs)), "file has", "files have"))
	for i, c := range cs {
		if i == clearsShown {
			fmt.Fprintf(w, "  and %s more\n", report.Count(int64(len(cs)-clearsShown)))
			break
		}
		where := c.Path
		if where == "" {
			where = fmt.Sprintf("file %d", c.FileID)
		}
		fmt.Fprintf(w, "  %s  %s\n", c.KeepUntil, where)
	}
	fmt.Fprintf(w, "Clear the keep_until date of %s? [y/n] ", report.Plural(int64(len(cs)), "file", "files"))
	answer, err := bufio.NewReader(e.stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	return false
}

func (e *env) deleteList(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("delete")
	db := dbFlag(fs, dbBefore)
	pos, err := e.parse(fs, "delete", args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError{"delete takes no arguments"}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer s.Close()
	items, err := s.Files(ctx, today(), store.Filter{})
	if err != nil {
		return err
	}
	var expired []store.FileItem
	var bytes int64
	for _, it := range items {
		if it.Status == store.StatusExpired {
			expired = append(expired, it)
			if it.Kind == "file" {
				bytes += it.Size
			}
		}
	}
	w := e.stdout
	if len(expired) == 0 {
		fmt.Fprintln(w, "No files are past their keep_until date.")
		return nil
	}
	fmt.Fprintf(w, "Past their keep_until date: %s, %s\n", report.Plural(int64(len(expired)), "file", "files"),
		report.Size(bytes))
	for _, it := range expired {
		fmt.Fprintf(w, "  %s  %10s  %s\n", it.KeepUntil, report.Size(it.Size), it.AbsPath())
	}
	fmt.Fprintln(w, "filo deletes nothing in this version. To keep a file longer, give it a later keep_until "+
		"(filo export lifespan, then filo update).")
	return nil
}
