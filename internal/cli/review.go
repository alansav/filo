package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/review"
	"github.com/alansav/filo/internal/store"
)

// now is the clock for review dates; tests replace it.
var now = time.Now

var reviewCommands = []string{"export", "import", "set-date"}

func (e *env) review(ctx context.Context, dbBefore string, args []string) error {
	sub, rest := splitSubcommand(args)
	switch sub {
	case "export":
		return e.reviewExport(ctx, dbBefore, rest)
	case "import":
		return e.reviewImport(ctx, dbBefore, rest)
	case "set-date":
		return e.reviewSetDate(ctx, dbBefore, rest)
	}
	return e.reviewSummary(ctx, dbBefore, args)
}

// splitSubcommand finds a review subcommand among args, so it may follow a flag (filo review --db X export), and
// returns it with the other arguments. Only the first positional argument can be one; "" means none.
func splitSubcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return "", args
		}
		if strings.HasPrefix(a, "-") {
			if (a == "--db" || a == "-db") && i+1 < len(args) {
				i++ // its value
			}
			continue
		}
		if slices.Contains(reviewCommands, a) {
			return a, append(slices.Clone(args[:i]), args[i+1:]...)
		}
		return "", args
	}
	return "", args
}

// filterFor maps an optional PATH to a queue filter.
func filterFor(ctx context.Context, s *store.Store, pos []string) (store.QueueFilter, string, error) {
	if len(pos) == 0 {
		return store.QueueFilter{}, "", nil
	}
	root, scope, err := s.Resolve(ctx, pos[0])
	if err != nil {
		return store.QueueFilter{}, "", err
	}
	where := root.Path
	if scope != "" {
		where = filepath.Join(root.Path, filepath.FromSlash(scope))
	}
	return store.QueueFilter{RootID: root.ID, Scope: scope}, where, nil
}

func today() string { return review.Today(now().Local()) }

func (e *env) reviewSummary(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("review")
	db := dbFlag(fs, dbBefore)
	top := fs.Int("top", 15, "")
	depth := fs.Int("depth", 2, "")
	pos, err := e.parse(fs, "review", args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError{"review takes at most one PATH"}
	}
	if err := checkTildes("PATH", pos...); err != nil {
		return err
	}
	if *top < 1 || *depth < 1 {
		return usageError{"--top and --depth must be at least 1"}
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
	q, err := s.Queue(ctx, today(), f)
	if err != nil {
		return err
	}
	w := e.stdout
	if where != "" {
		where = " under " + where
	}
	if len(q) == 0 {
		fmt.Fprintf(w, "Review queue%s: empty\n", where)
		return nil
	}
	counts := map[store.Reason]int64{}
	var bytes int64
	for _, it := range q {
		counts[it.Reason]++
		bytes += fileBytes(it)
	}
	fmt.Fprintf(w, "Review queue%s: %s, %s\n", where, report.Plural(int64(len(q)), "file", "files"), report.Size(bytes))
	for _, r := range store.Reasons {
		if counts[r] > 0 {
			fmt.Fprintf(w, "  %-22s %8s\n", report.ReasonLabel(r), report.Count(counts[r]))
		}
	}

	fmt.Fprintf(w, "\nFolders with the most queued files (to depth %d):\n", *depth)
	for _, g := range folderGroups(q, *depth, *top) {
		fmt.Fprintf(w, "  %8s  %10s  %s\n", report.Count(g.files), report.Size(g.bytes), g.dir)
	}
	fmt.Fprint(w, `
Give whole folders a first review date:  filo review set-date FOLDER DATE   (DATE: 2027-06-30, +6m, +2y)
Then export what is left:                filo review export -o review.csv
`)
	return nil
}

// fileBytes is the item's size if it is a regular file: a symlink's size is only the length of its target.
func fileBytes(it store.QueueItem) int64 {
	if it.Kind == "file" {
		return it.Size
	}
	return 0
}

type folderGroup struct {
	dir          string
	files, bytes int64
}

// folderGroups counts queued files by their folder, cut at depth levels below the root, largest count first.
func folderGroups(q []store.QueueItem, depth, top int) []folderGroup {
	m := map[string]*folderGroup{}
	for _, it := range q {
		parts := strings.Split(it.Path, "/")
		dirs := parts[:len(parts)-1]
		if len(dirs) > depth {
			dirs = dirs[:depth]
		}
		dir := filepath.Join(append([]string{it.RootPath}, dirs...)...)
		g := m[dir]
		if g == nil {
			g = &folderGroup{dir: dir}
			m[dir] = g
		}
		g.files++
		g.bytes += fileBytes(it)
	}
	out := make([]folderGroup, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b folderGroup) int {
		if a.files != b.files {
			if a.files > b.files {
				return -1
			}
			return 1
		}
		return strings.Compare(a.dir, b.dir)
	})
	if len(out) > top {
		out = out[:top]
	}
	return out
}

func (e *env) reviewExport(ctx context.Context, dbBefore string, args []string) error {
	fl := e.flags("review export")
	db := dbFlag(fl, dbBefore)
	out := fl.String("o", "", "")
	pos, err := e.parse(fl, "review", args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError{"review export takes at most one PATH"}
	}
	if err := checkTildes("PATH", pos...); err != nil {
		return err
	}
	if err := checkTilde("-o", *out); err != nil {
		return err
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer s.Close()
	f, _, err := filterFor(ctx, s, pos)
	if err != nil {
		return err
	}
	q, err := s.Queue(ctx, today(), f)
	if err != nil {
		return err
	}
	msg := e.stderr // the CSV may be on stdout
	if *out != "" {
		msg = e.stdout
	}
	if len(q) == 0 {
		fmt.Fprintln(msg, "The review queue is empty; nothing exported.")
		return nil
	}

	if *out == "" {
		id, err := s.RecordExport(ctx, "", now(), q, func(id int64) error {
			return review.WriteCSV(e.stdout, id, q, time.Local)
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(msg, "Exported %s (export %d).\n", report.Plural(int64(len(q)), "file", "files"), id)
		return nil
	}

	path, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	// O_EXCL: an existing file is never overwritten, whatever it is.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists; choose another name (filo never overwrites a file)", path)
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	id, err := s.RecordExport(ctx, path, now(), q, func(id int64) error {
		bw := bufio.NewWriter(file)
		if err := review.WriteCSV(bw, id, q, time.Local); err != nil {
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
	fmt.Fprintf(msg, "Exported %s to %s (export %d).\n", report.Plural(int64(len(q)), "file", "files"), path, id)
	fmt.Fprintf(msg, "Fill in review_date (2027-06-30, +30d, +12m, +7y) or action (delete), then run:\n"+
		"  filo review import %s\n", path)
	return nil
}

func (e *env) reviewImport(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("review import")
	db := dbFlag(fs, dbBefore)
	dry := fs.Bool("dry-run", false, "")
	pos, err := e.parse(fs, "review", args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{"review import takes exactly one FILE"}
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
	t := now()
	parsed, err := review.ReadCSV(file, t.Local())
	file.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer s.Close()
	res, err := s.ApplyDecisions(ctx, parsed.Decisions, t, today(), path, *dry)
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
	verb := "Applied"
	if *dry {
		fmt.Fprintln(w, "Dry run: nothing was written.")
		verb = "Would apply"
	}
	fmt.Fprintf(w, "%s %s: %s, %s.\n", verb, report.Plural(int64(res.Dates+res.Deletes), "decision", "decisions"),
		report.Plural(int64(res.Dates), "review date", "review dates"), report.Plural(int64(res.Deletes),
			"file marked delete", "files marked delete"))
	if res.Retained > 0 {
		fmt.Fprintf(w, "%s only a retain_until date: kept until then, when it comes back for review.\n",
			report.Plural(int64(res.Retained), "file has", "files have"))
	}
	if res.Deletes > 0 {
		fmt.Fprintln(w, "Marked delete is only recorded: filo deletes nothing in this version.")
	}
	var notes []string
	if n := parsed.Blank + res.Untouched; n > 0 {
		notes = append(notes, report.Plural(int64(n), "row", "rows")+" left blank")
	}
	if res.Already > 0 {
		notes = append(notes, report.Plural(int64(res.Already), "row", "rows")+" already imported")
	}
	if len(bad) > 0 {
		notes = append(notes, report.Plural(int64(len(bad)), "row", "rows")+" refused (listed above)")
	}
	if len(notes) > 0 {
		fmt.Fprintf(w, "%s.\n", strings.Join(notes, ", "))
	}
	if len(parsed.Decisions) == 0 && len(bad) == 0 {
		fmt.Fprintln(w, "No row has a review_date, action or retain_until filled in. Those are the only columns filo "+
			"reads (G, H and I as exported).")
	}
	if len(bad) > 0 {
		return errReported
	}
	return nil
}

func (e *env) reviewSetDate(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("review set-date")
	db := dbFlag(fs, dbBefore)
	dry := fs.Bool("dry-run", false, "")
	pos, err := e.parse(fs, "review", args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageError{"review set-date takes a FOLDER and a DATE"}
	}
	if err := checkTilde("FOLDER", pos[0]); err != nil {
		return err
	}
	t := now()
	date, err := review.ParseDate(pos[1], t.Local())
	if err != nil {
		return usageError{"DATE " + err.Error()}
	}
	if date <= today() {
		return usageError{fmt.Sprintf("DATE %s is not after today", date)}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer s.Close()
	f, where, err := filterFor(ctx, s, pos[:1])
	if err != nil {
		return err
	}
	n, err := s.SetReviewDate(ctx, f, date, t, where+" "+date, *dry)
	if err != nil {
		return err
	}
	w := e.stdout
	switch {
	case n == 0:
		fmt.Fprintf(w, "Every file under %s already has a review date or a decision; nothing changed.\n", where)
		return nil
	case *dry:
		fmt.Fprintf(w, "Dry run: would set review date %s on %s under %s.\n", date, report.Plural(n, "file", "files"), where)
		return nil
	}
	fmt.Fprintf(w, "Set review date %s on %s under %s.\n", date, report.Plural(n, "file", "files"), where)
	q, err := s.Queue(ctx, today(), store.QueueFilter{})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "The review queue now has %s.\n", report.Plural(int64(len(q)), "file", "files"))
	return nil
}
