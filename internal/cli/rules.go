package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/rules"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// filo rules (docs/design.md, Lifespan, Directory rules): a policy for the files under a directory. With no
// subcommand it lists the rules, at DIR and below if a directory is given; add, set and remove act on one. No
// command asks: add says how many files it dated, and --dry-run shows them first.

// rulesUsage is the one line the wrong number of arguments gets.
const rulesUsage = "filo rules [DIR] | add DIR [PATTERN] POLICY [--dry-run] | set DIR [PATTERN] POLICY | " +
	"remove DIR [PATTERN]; quote a pattern with * or spaces ('*Statement*')"

func (e *env) rules(ctx context.Context, dbBefore string, args []string) error {
	sub, rest := rootsSub(args)
	fl := e.flags("rules")
	db := dbFlag(fl, dbBefore)
	dry := fl.Bool("dry-run", false, "")
	pos, err := e.parse(fl, "rules", rest)
	if err != nil {
		return err
	}
	want := map[string][2]int{"": {0, 1}, "add": {2, 3}, "set": {2, 3}, "remove": {1, 2}}
	if _, known := want[sub]; !known {
		if len(pos) > 0 {
			return usageError{fmt.Sprintf("unknown rules command %q; see filo help rules", sub)}
		}
		sub, pos = "", []string{sub} // filo rules DIR
	}
	n := want[sub]
	switch {
	case len(pos) < n[0] || len(pos) > n[1]:
		return usageError{"wrong number of arguments: " + rulesUsage}
	case *dry && sub != "add":
		return usageError{"--dry-run is for rules add"}
	}
	if len(pos) > 0 {
		if err := checkTilde("DIR", pos[0]); err != nil {
			return err
		}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	// PATTERN may be left out: the last argument is the policy, and a pattern sits between DIR and it.
	pattern := ""
	switch sub {
	case "add", "set":
		if len(pos) == 3 {
			pattern = pos[1]
		}
		if sub == "add" {
			return e.ruleAdd(ctx, s, pos[0], pattern, pos[len(pos)-1], *dry)
		}
		return e.ruleSet(ctx, s, pos[0], pattern, pos[len(pos)-1])
	case "remove":
		if len(pos) == 2 {
			pattern = pos[1]
		}
		return e.ruleRemove(ctx, s, pos[0], pattern)
	}
	return e.ruleList(ctx, s, pos)
}

// ruleDir finds the root and the directory relative to it that DIR names. A directory on disk goes through
// store.Resolve, which also refuses a root that cannot be reached (not mounted): rules are not applied while its files
// cannot be checked. With gone, a directory no longer on disk is found by its path alone (store.Locate), so a rule
// whose directory went can still be changed, removed or listed.
func ruleDir(ctx context.Context, s *store.Store, arg string, gone bool) (store.Root, string, error) {
	p, err := filepath.Abs(arg)
	if err != nil {
		return store.Root{}, "", err
	}
	fi, statErr := os.Stat(p)
	switch {
	case statErr == nil && !fi.IsDir():
		return store.Root{}, "", fmt.Errorf("%s is not a directory: a rule is for a directory's files", p)
	case statErr == nil || !gone:
		return s.Resolve(ctx, p)
	}
	return s.Locate(ctx, p)
}

// ruleWhere names a rule's directory in a line: relative to its root, or the root itself for "".
func (e *env) ruleWhere(root, dir string) string {
	if dir == "" {
		return e.view.Tilde(root)
	}
	return dir
}

// ruleTo is a rule's pattern and policy as a line shows them: "*Statement* -> statements (7y)".
func ruleTo(pattern, policy, value string) string {
	s := pattern + " -> " + policy
	if value != "" {
		s += " (" + value + ")"
	}
	return s
}

// ruleErr turns the store's refusals into one clear line naming the directory and the pattern as typed.
func ruleErr(err error, where, pattern string) error {
	p, perr := rules.Pattern(pattern)
	if perr != nil {
		p = pattern
	}
	switch {
	case errors.Is(err, store.ErrRuleExists):
		return fmt.Errorf("%s: already has the rule %s (filo rules set changes its policy)", where, p)
	case errors.Is(err, store.ErrNoRule):
		return fmt.Errorf("%s: no rule %s there (filo rules lists them)", where, p)
	case errors.Is(err, rules.ErrPattern):
		return fmt.Errorf("%q: %w", pattern, err)
	}
	return err
}

func (e *env) ruleAdd(ctx context.Context, s *store.Store, arg, pattern, policy string, dry bool) error {
	root, dir, err := ruleDir(ctx, s, arg, false)
	if err != nil {
		return err
	}
	add := s.AddRule
	if dry {
		add = s.PreviewAddRule
	}
	rule, created, run, err := add(ctx, root.ID, dir, pattern, policy, today(), now())
	where := e.ruleWhere(root.Path, dir)
	if err != nil {
		return ruleErr(err, where, pattern)
	}
	r := e.view
	needs := 0
	for _, d := range run.Dated {
		if d.NeedsFileDate() {
			needs++
		}
	}
	title, dated := "Done", " dated"
	if dry {
		title, dated = "Dry run", " would be dated"
	}
	facts := []string{where, ruleTo(rule.Pattern, rule.Policy, rule.PolicyValue),
		report.Plural(int64(len(run.Dated)), "file", "files") + dated}
	if needs > 0 {
		facts = append(facts, report.Plural(int64(needs), "needs a file_date", "need a file_date"))
	}
	r.Heading(title, view.Where{Roots: []string{root.Path}}, facts...)
	if created {
		was := "was"
		if dry {
			was = "would be"
		}
		r.Count(view.Neutral, "policy "+rule.Policy+" "+was+" created with no value: its files need a retention "+
			"until it has one")
	}
	if dry && len(run.Dated) > 0 {
		r.Blank()
		r.Files(view.FileList{Columns: []view.Column{{Header: "file_date"}, {Header: "until"}},
			Rows: datedRows(run.Dated), Max: r.Overview(), More: "files", MoreOne: "file"})
	}
	e.ruleTies(run.Ties)
	next := ""
	switch {
	case dry:
		next = "Dry run: nothing was changed."
	case created:
		next = "Next: filo policies set " + rule.Policy + " VALUE"
	case needs > 0:
		next = "Next: filo export retention " + shellQuote(store.AbsPath(root.Path, dir)) +
			" -o FILE, fill in file_date, then filo update FILE"
	}
	if next != "" {
		r.Blank()
		r.Next(next)
	}
	return nil
}

// datedRows are files a rule dated as file rows: their file_date and their keep_until now.
func datedRows(ds []store.RuleDated) []view.FileRow {
	rows := make([]view.FileRow, len(ds))
	for i, d := range ds {
		until := d.KeepUntil
		switch {
		case d.NeedsFileDate():
			until = "needs a file_date"
		case d.PolicyValue == "":
			until = "no value yet"
		}
		rows[i] = view.FileRow{Root: d.RootPath, Rel: d.Path, Cells: []string{d.FileDate, until}}
	}
	return rows
}

// ruleTies says which files no rule dates because rules match them equally, or their hard links are decided by
// different rules, grouped by the rules involved (in any order) and listing each file once.
func (e *env) ruleTies(ties []store.RuleTie) {
	type group struct {
		t     store.RuleTie // the first: its rules and why
		files []view.FileRow
	}
	var groups []*group
	byKey := map[string]*group{}
	seen := map[int64]bool{}
	for _, t := range ties {
		if seen[t.FileID] { // a file with paths in two roots comes once per root
			continue
		}
		seen[t.FileID] = true
		var ids []int64
		for _, r := range t.Rules {
			ids = append(ids, r.ID)
		}
		slices.Sort(ids)
		key := fmt.Sprint(t.HardLinks, ids)
		g, ok := byKey[key]
		if !ok {
			g = &group{t: t}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.files = append(g.files, view.FileRow{Root: t.RootPath, Rel: t.Path})
	}
	r := e.view
	for _, g := range groups {
		n := int64(len(g.files))
		var what string
		if g.t.HardLinks {
			var each []string
			for _, x := range g.t.Rules {
				each = append(each, e.ruleWhere(x.RootPath, x.Dir)+" "+x.Pattern)
			}
			what = report.Plural(n, "file has hard links", "files have hard links") + " that different rules decide (" +
				strings.Join(each, ", ") + ")"
		} else {
			var patterns []string
			for _, x := range g.t.Rules {
				patterns = append(patterns, x.Pattern)
			}
			first := g.t.Rules[0]
			what = report.Plural(n, "file matches", "files match") + " " + report.Count(int64(len(patterns))) +
				" rules of " + e.ruleWhere(first.RootPath, first.Dir) + " equally (" + strings.Join(patterns, ", ") + ")"
		}
		r.Blank()
		r.Note(view.Attention, "Not applied   "+what)
		r.Files(view.FileList{Rows: g.files, Max: r.Overview(), More: "files", MoreOne: "file"})
	}
}

func (e *env) ruleSet(ctx context.Context, s *store.Store, arg, pattern, policy string) error {
	root, dir, err := ruleDir(ctx, s, arg, true)
	if err != nil {
		return err
	}
	where := e.ruleWhere(root.Path, dir)
	old, created, err := s.SetRule(ctx, root.ID, dir, pattern, policy, now())
	if err != nil {
		return ruleErr(err, where, pattern)
	}
	name, _ := store.PolicyName(policy)
	r := e.view
	if name == old.Policy {
		r.Outcome("Unchanged", where, ruleTo(old.Pattern, old.Policy, "")+" already")
		return nil
	}
	r.Outcome("Done", where, ruleTo(old.Pattern, name, "")+" (was "+old.Policy+")")
	if old.Dated > 0 {
		// Not the rule's policy by name: a rule set before may have dated them with another.
		r.Count(view.Neutral, report.Plural(old.Dated, "file", "files")+" it dated "+verb(old.Dated, "keeps its",
			"keep their")+" policy: the change is for new files")
	}
	if created {
		r.Count(view.Neutral, "policy "+name+" was created with no value: its files need a retention until it has one")
		r.Blank()
		r.Next("Next: filo policies set " + name + " VALUE")
	}
	return nil
}

func (e *env) ruleRemove(ctx context.Context, s *store.Store, arg, pattern string) error {
	root, dir, err := ruleDir(ctx, s, arg, true)
	if err != nil {
		return err
	}
	where := e.ruleWhere(root.Path, dir)
	old, err := s.RemoveRule(ctx, root.ID, dir, pattern)
	if err != nil {
		return ruleErr(err, where, pattern)
	}
	r := e.view
	r.Outcome("Removed", where, ruleTo(old.Pattern, old.Policy, ""))
	if old.Dated > 0 {
		r.Count(view.Neutral, report.Plural(old.Dated, "file", "files")+" it dated "+verb(old.Dated, "keeps its",
			"keep their")+" policy")
	}
	return nil
}

func (e *env) ruleList(ctx context.Context, s *store.Store, pos []string) error {
	var rootID int64
	dir := ""
	var where view.Where
	if len(pos) == 1 {
		root, d, err := ruleDir(ctx, s, pos[0], true)
		if err != nil {
			return err
		}
		rootID, dir = root.ID, d
		where = view.Where{Roots: []string{root.Path}, Under: d}
	}
	rs, err := s.Rules(ctx, rootID, dir)
	if err != nil {
		return err
	}
	r := e.view
	if len(rs) == 0 {
		none := "none yet"
		if len(pos) == 1 {
			none = "none at or below it (the rules above it are listed by filo rules)"
		}
		r.Heading("Rules", where, none)
		r.Blank()
		r.Next("Next: filo rules add DIR [PATTERN] POLICY")
		return nil
	}
	roots, err := s.Roots(ctx)
	if err != nil {
		return err
	}
	byID := map[int64]store.Root{}
	for _, ro := range roots {
		byID[ro.ID] = ro
	}
	// The rules come ordered by root, then directory: a group per directory, a section per root.
	type section struct {
		root   store.Root
		groups []view.TableGroup
	}
	var sections []*section
	reach := map[int64]error{} // each root checked once
	dirs, attention := 0, 0
	for _, x := range rs {
		if len(sections) == 0 || sections[len(sections)-1].root.ID != x.RootID {
			sections = append(sections, &section{root: byID[x.RootID]})
		}
		sec := sections[len(sections)-1]
		if n := len(sec.groups); n == 0 || sec.groups[n-1].Name != e.ruleGroupName(x.Dir) {
			if _, ok := reach[x.RootID]; !ok {
				reach[x.RootID] = store.CheckRoot(sec.root)
			}
			g := view.TableGroup{Name: e.ruleGroupName(x.Dir), Attention: ruleDirAttention(sec.root, x.Dir,
				reach[x.RootID])}
			if g.Attention != "" {
				attention++
			}
			sec.groups = append(sec.groups, g)
			dirs++
		}
		row := view.TableRow{Name: x.Pattern, Cells: []string{x.Policy, valueWord(x.PolicyValue),
			report.Plural(x.Dated, "file", "files")}}
		if x.PolicyValue == "" {
			row.Attention = "needs a retention"
			attention++
		}
		g := &sec.groups[len(sec.groups)-1]
		g.Rows = append(g.Rows, row)
	}
	if len(pos) == 0 {
		for _, sec := range sections {
			where.Roots = append(where.Roots, sec.root.Path)
		}
	}
	facts := []string{report.Count(int64(len(rs))), report.Plural(int64(dirs), "directory", "directories")}
	if attention > 0 {
		facts = append(facts, report.Plural(int64(attention), "needs attention", "need attention"))
	}
	r.Heading("Rules", where, facts...)
	day := today()
	var ties []store.RuleTie
	for _, sec := range sections {
		r.Blank()
		if len(sections) > 1 {
			r.Entry(0, sec.root.Path)
		}
		r.Groups(sec.groups, false, false, true)
		ts, err := s.RuleTies(ctx, sec.root.ID, day, now())
		if err != nil {
			return err
		}
		for _, t := range ts {
			if dir == "" || t.Path == dir || strings.HasPrefix(t.Path, dir+"/") {
				ties = append(ties, t)
			}
		}
	}
	e.ruleTies(ties)
	r.Blank()
	r.Next("Next: filo rules add DIR [PATTERN] POLICY, filo rules remove DIR [PATTERN]")
	return nil
}

// ruleGroupName names a rule's directory in the list: relative to its root, "." for the root itself.
func (e *env) ruleGroupName(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

// ruleDirAttention checks a rule's directory on disk: the register keeps no directories, so an empty one still
// counts. A root that cannot be reached (reach, from store.CheckRoot) cannot be checked.
func ruleDirAttention(root store.Root, dir string, reach error) string {
	if reach != nil {
		return "cannot be checked: its root is not reachable"
	}
	fi, err := os.Stat(store.AbsPath(root.Path, dir))
	switch {
	case errors.Is(err, os.ErrNotExist) || (err == nil && !fi.IsDir()):
		return "no such directory"
	case err != nil:
		return "cannot be checked: " + err.Error()
	}
	return ""
}
