package cli

import (
	"context"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/alansav/filo/internal/hasher"
	"github.com/alansav/filo/internal/reconcile"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
	"github.com/alansav/filo/internal/walk"
)

// valueFlags are the roots flags that take a value: given as the next argument (--ignore build), that value is not
// a subcommand.
var valueFlags = map[string]bool{"db": true, "ignore": true, "workers": true}

// rootsSub finds a roots subcommand among args, so it may follow a flag (filo roots --db X add DIR), and returns it
// with the other arguments. Only the first positional argument can be one; "" means none.
func rootsSub(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return "", args
		}
		if strings.HasPrefix(a, "-") {
			if valueFlags[strings.TrimLeft(a, "-")] && i+1 < len(args) {
				i++ // its value, given as the next argument
			}
			continue
		}
		return a, append(slices.Clone(args[:i]), args[i+1:]...)
	}
	return "", args
}

func (e *env) roots(ctx context.Context, dbBefore string, args []string) error {
	sub, rest := rootsSub(args)
	switch sub {
	case "":
		return e.rootsList(ctx, dbBefore, rest)
	case "add":
		return e.rootsAdd(ctx, dbBefore, rest)
	case "set":
		return e.rootsSet(ctx, dbBefore, rest)
	}
	return usageError{fmt.Sprintf("unknown roots command %q", sub)}
}

// rootsList shows every root as the report's Roots section does (docs/design.md, Terminal output, Scan and roots).
func (e *env) rootsList(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("roots")
	db := dbFlag(fs, dbBefore)
	pos, err := e.parse(fs, "roots", args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usageError{"unknown roots command"}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	totals, err := s.RootTotals(ctx)
	if err != nil {
		return err
	}
	v := e.view
	if len(totals) == 0 {
		v.Heading("Roots", view.Where{}, "none yet")
		v.Next("Next: filo roots add DIR")
		return nil
	}
	var paths []string
	var files, bytes int64
	for _, t := range totals {
		paths = append(paths, t.Root.Path)
		files += t.Files
		bytes += t.Bytes
	}
	v.Heading("Roots", view.Where{Roots: paths}, report.Plural(files, "file", "files"), report.Size(bytes))
	v.Blank()
	report.RootEntries(v, totals)
	return nil
}

// settingsFlags adds the root settings flags to fs. skip adds --skip-hidden too (roots set).
func settingsFlags(fs *flag.FlagSet, skip bool) (include, skipHidden *bool, ignore *stringList) {
	include = fs.Bool("include-hidden", false, "")
	if skip {
		skipHidden = fs.Bool("skip-hidden", false, "")
	}
	ignore = &stringList{}
	fs.Var(ignore, "ignore", "")
	return include, skipHidden, ignore
}

func checkIgnores(names []string) error {
	for _, n := range names {
		if err := walk.CheckIgnore(n); err != nil {
			return usageError{"--" + err.Error()}
		}
	}
	return nil
}

func (e *env) rootsAdd(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("roots add")
	db := dbFlag(fs, dbBefore)
	include, _, ignore := settingsFlags(fs, false)
	noScan := fs.Bool("no-scan", false, "")
	workers := fs.Int("workers", reconcile.DefaultWorkers, "")
	pos, err := e.parse(fs, "roots", args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{"roots add takes exactly one DIR"}
	}
	if err := checkTilde("DIR", pos[0]); err != nil {
		return err
	}
	if err := checkIgnores(*ignore); err != nil {
		return err
	}
	if *workers < 1 || *workers > hasher.MaxWorkers {
		return usageError{fmt.Sprintf("--workers must be between 1 and %d", hasher.MaxWorkers)}
	}
	set := store.Settings{IncludeHidden: *include, Ignore: *ignore}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	r, absorbed, err := s.AddRootAbsorb(ctx, pos[0], set)
	if err != nil {
		return err
	}
	suggested := renderAdded(e.view, r, absorbed)
	if *noScan && suggested {
		return nil // its Next line, roots set, comes first: a scan follows it anyway
	}
	if *noScan {
		e.view.Next("Next: filo scan " + shellQuote(r.Path))
		return nil
	}
	e.view.Blank()
	if err := e.scanTarget(ctx, s, reconcile.Options{Target: r.Path, Workers: *workers}); err != nil {
		if ctx.Err() != nil { // interrupted, whatever error that surfaced as (SQLite has its own)
			return fmt.Errorf("scan interrupted; the root is added, nothing from the scan was written: filo scan %s",
				shellQuote(r.Path))
		}
		return err
	}
	return nil
}

// renderAdded shows a new root: its settings and mount point, the roots it took in (a different setting is
// information; one the new settings skip needs attention, with the roots set command that would scan it again).
func renderAdded(v *view.Renderer, r store.Root, absorbed []store.Absorbed) (suggested bool) {
	v.Heading("Root added", view.Where{Roots: []string{r.Path}})
	v.Count(view.Neutral, v.Join(report.SettingsParts(r.Settings)...))
	if r.MountPath != "" {
		v.CountName(view.Neutral, "on the filesystem mounted at ", r.MountPath,
			": scans are refused while it is not mounted")
	}
	if len(absorbed) == 0 {
		return false
	}
	v.Blank()
	v.Section(fmt.Sprintf("Took in %s, with %s history and retentions",
		report.Plural(int64(len(absorbed)), "root", "roots"), pluralWord(len(absorbed), "its", "their")))
	fix := r.Settings
	fix.Ignore = slices.Clone(r.Settings.Ignore)
	var skipped []string
	for _, a := range absorbed {
		where := v.Path(r.Path, a.Prefix)
		switch {
		case skippedBy(a.Prefix, r.Settings):
			v.CountName(view.Attention, "", where,
				"   skipped by these settings: its files are neither checked nor marked missing")
			try := fix
			try.Ignore = slices.Clone(fix.Ignore)
			if unskip(&try, a.Prefix) { // a root only an always-skipped name hides leaves the settings alone
				fix = try
				skipped = append(skipped, where)
			}
		case !a.Settings.Equal(r.Settings):
			v.CountName(view.Neutral, "", where, "   had other settings ("+
				v.Join(report.SettingsParts(a.Settings)...)+"): these apply now")
		default:
			v.CountName(view.Neutral, "", where, "")
		}
	}
	if len(skipped) > 0 {
		v.Next("Next: " + setCommand(r.Path, fix) + ", to scan " + joinAnd(skipped) + " again")
	}
	return len(skipped) > 0
}

// unskip changes set so that a scan no longer skips the folder at prefix, if a setting skips it (a hidden folder, an
// ignored name); it reports false if only a name filo always skips does (node_modules), which no setting changes.
// It may change set either way: the caller passes a copy and keeps it only on true.
func unskip(set *store.Settings, prefix string) bool {
	for _, name := range strings.Split(prefix, "/") {
		if i := slices.Index(set.Ignore, name); i >= 0 {
			set.Ignore = slices.Delete(set.Ignore, i, i+1)
		}
		if !set.IncludeHidden && walk.Skips(name, false, nil) && !walk.Skips(name, true, nil) {
			set.IncludeHidden = true
		}
	}
	return !skippedBy(prefix, *set)
}

// setCommand is the filo roots set command that gives the root at path these settings, whole: roots set replaces
// them, so every --ignore to keep is named.
func setCommand(path string, set store.Settings) string {
	cmd := "filo roots set " + shellQuote(path)
	if set.IncludeHidden {
		cmd += " --include-hidden"
	}
	for _, name := range set.Ignore {
		cmd += " --ignore " + shellQuote(name)
	}
	return cmd
}

func (e *env) rootsSet(ctx context.Context, dbBefore string, args []string) error {
	fs := e.flags("roots set")
	db := dbFlag(fs, dbBefore)
	include, skip, ignore := settingsFlags(fs, true)
	pos, err := e.parse(fs, "roots", args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{"roots set takes exactly one DIR"}
	}
	if *include && *skip {
		return usageError{"give --include-hidden or --skip-hidden, not both"}
	}
	if err := checkTilde("DIR", pos[0]); err != nil {
		return err
	}
	if err := checkIgnores(*ignore); err != nil {
		return err
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	// Count what the new settings skip before writing them, so a failure here changes nothing.
	set := store.Settings{IncludeHidden: *include, Ignore: *ignore}
	cur, err := s.RootAt(ctx, pos[0])
	if err != nil {
		return err
	}
	newlySkipped, err := s.CountPaths(ctx, cur.ID, func(p string) bool {
		return skippedBy(p, set) && !skippedBy(p, cur.Settings)
	})
	if err != nil {
		return err
	}
	old, now, err := s.SetSettings(ctx, pos[0], set)
	if err != nil {
		return err
	}
	v := e.view
	if now.Settings.Equal(old.Settings) {
		v.Heading("Settings unchanged", view.Where{Roots: []string{now.Path}})
		v.Count(view.Neutral, v.Join(report.SettingsParts(now.Settings)...))
		return nil
	}
	v.Heading("Settings changed", view.Where{Roots: []string{now.Path}})
	v.Count(view.Neutral, "was   "+v.Join(report.SettingsParts(old.Settings)...))
	v.Count(view.Done, "now   "+v.Join(report.SettingsParts(now.Settings)...))
	if newlySkipped > 0 {
		v.Count(view.Attention, report.Plural(newlySkipped, "registered file or symlink is", "registered files and symlinks are")+
			" now skipped: neither checked nor marked missing")
	}
	if more := covered(old.Settings, now.Settings); more != "" {
		v.Count(view.Neutral, "the next scan also covers "+more+": any not registered yet are new files and will "+
			"need a retention")
	}
	v.Next("Next: filo scan " + shellQuote(now.Path))
	return nil
}

// covered says what a scan with now covers that one with old skipped: "hidden files", "entries named build", or
// both; "" if nothing.
func covered(old, now store.Settings) string {
	var parts []string
	if now.IncludeHidden && !old.IncludeHidden {
		parts = append(parts, "hidden files")
	}
	var names []string
	for _, n := range old.Ignore {
		if !slices.Contains(now.Ignore, n) {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		parts = append(parts, "entries named "+joinAnd(names))
	}
	return joinAnd(parts)
}

// skippedBy reports whether a scan with set skips something on this '/'-separated path inside a root.
func skippedBy(prefix string, set store.Settings) bool {
	for _, name := range strings.Split(prefix, "/") {
		if walk.Skips(name, set.IncludeHidden, set.Ignore) {
			return true
		}
	}
	return false
}

// shellQuote writes a path or name in a Next line so it can be pasted into a shell: as it is when it holds only
// letters, digits and /._-, otherwise in single quotes (a single quote inside closes them, adds an escaped quote and
// reopens them). A path is never shown with ~ here, since filo refuses a quoted ~.
func shellQuote(s string) string {
	plain := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-", r)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
