package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// policies is filo policies: named retention policies (docs/design.md, Lifespan, Named retention policies). With no
// subcommand it lists them; add, set, move, remove and show act on one.
func (e *env) policies(ctx context.Context, dbBefore string, args []string) error {
	sub, rest := rootsSub(args)
	fl := e.flags("policies")
	db := dbFlag(fl, dbBefore)
	yes := fl.Bool("y", false, "")
	pos, err := e.parse(fl, "policies", rest)
	if err != nil {
		return err
	}
	want := map[string][2]int{"": {0, 0}, "add": {1, 2}, "set": {2, 2}, "move": {2, 2}, "remove": {1, 1},
		"show": {1, 1}}
	n, known := want[sub]
	switch {
	case !known:
		return usageError{fmt.Sprintf("unknown policies command %q", sub)}
	case len(pos) < n[0] || len(pos) > n[1]:
		return usageError{"wrong number of arguments for filo policies " + sub + "; see filo help policies"}
	case *yes && sub != "set" && sub != "move":
		return usageError{"-y is for policies set and move"}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	switch sub {
	case "add":
		value := ""
		if len(pos) == 2 {
			if value, err = policyValue(pos[1]); err != nil {
				return err
			}
		}
		return e.policyAdd(ctx, s, pos[0], value)
	case "set":
		value, err := policyValue(pos[1])
		if err != nil {
			return err
		}
		return e.policySet(ctx, s, pos[0], value, *yes)
	case "move":
		return e.policyMove(ctx, s, pos[0], pos[1], *yes)
	case "remove":
		return e.policyRemove(ctx, s, pos[0])
	case "show":
		return e.policyShow(ctx, s, pos[0])
	}
	return e.policyList(ctx, s)
}

// policyValue reads a typed policy value: a period, a date or forever (a policy name is not one).
func policyValue(typed string) (string, error) {
	v, err := retention.ParseRetention(typed)
	if err != nil || v == "" || store.IsPolicyRef(v) {
		return "", fmt.Errorf("%q is not a policy value: a period such as 7y, a date YYYY-MM-DD, or forever", typed)
	}
	return v, nil
}

// valueWord is a policy's value as shown, "no value" for none yet.
func valueWord(v string) string {
	if v == "" {
		return "no value"
	}
	return v
}

// policyWhat words a policy's value in full: "7y from each file's file_date", "forever", "2030-12-31", "no value".
func policyWhat(value string) string {
	if store.IsPeriod(value) {
		return value + " from each file's file_date"
	}
	return valueWord(value)
}

func (e *env) policyList(ctx context.Context, s *store.Store) error {
	ps, err := s.Policies(ctx)
	if err != nil {
		return err
	}
	r := e.view
	if len(ps) == 0 {
		r.Heading("Policies", view.Where{}, "none yet")
		r.Blank()
		r.Next("Next: filo policies add NAME VALUE, or type a policy name in the retention column of a CSV from filo export")
		return nil
	}
	day := today() // one date for the whole list
	rows := make([]view.TableRow, len(ps))
	attention := int64(0)
	first := ""
	for i, p := range ps {
		detail := ""
		if store.IsPeriod(p.Value) {
			detail = "from each file's file_date"
		}
		rows[i] = view.TableRow{Name: p.Name, Cells: []string{valueWord(p.Value), detail,
			report.Plural(p.Files, "file", "files")}, Attention: report.PolicyAttention(p, day)}
		if rows[i].Attention != "" {
			attention++
			if first == "" {
				first = p.Name
			}
		}
	}
	facts := []string{report.Count(int64(len(ps)))}
	if attention > 0 {
		facts = append(facts, report.Plural(attention, "needs attention", "need attention"))
	}
	r.Heading("Policies", view.Where{}, facts...)
	r.Blank()
	r.Table(rows, false, false, true)
	r.Blank()
	if first != "" {
		r.Next("Next: filo policies show " + first + ", then filo policies set " + first + " VALUE or filo policies " +
			"move " + first + " NAME")
	} else {
		r.Next("Next: filo policies show NAME for a policy's files")
	}
	return nil
}

func (e *env) policyAdd(ctx context.Context, s *store.Store, name, value string) error {
	err := s.AddPolicy(ctx, name, value, now())
	if errors.Is(err, store.ErrPolicyExists) {
		n, _ := store.PolicyName(name)
		return fmt.Errorf("%s: a policy of that name exists already (filo policies set %s VALUE changes it)", n, n)
	}
	if err != nil {
		return err
	}
	n, _ := store.PolicyName(name)
	e.view.Outcome("Added", n, valueWord(value))
	return nil
}

// policyRows are the files of a preview as file rows: their file_date and their new keep_until.
func policyRows(fs []store.PolicyFile) []view.FileRow {
	rows := make([]view.FileRow, len(fs))
	for i, f := range fs {
		until := f.KeepUntil
		if until == "" {
			until = "needs a file_date"
		}
		rows[i] = view.FileRow{Root: f.RootPath, Rel: f.Path, Cells: []string{f.FileDate, until}}
	}
	return rows
}

// needsAsking reports whether a change of policy needs a question: it puts files past their date, leaves files
// without a file_date to count from, or takes their keep_until away.
func needsAsking(p store.PolicyPreview) bool {
	return len(p.Past) > 0 || len(p.NoFileDate) > 0 || len(p.LoseDate) > 0
}

// showPreview writes what a change of policy would do that needs a question, each list cut to max entries (0: all).
// It reports whether a list was cut, so the question offers l.
func (e *env) showPreview(p store.PolicyPreview, max int) (cut bool) {
	r := e.view
	cols := []view.Column{{Header: "file_date"}, {Header: "until"}}
	list := func(fs []store.PolicyFile) {
		r.Files(view.FileList{Columns: cols, Rows: policyRows(fs), Max: max, More: "files", MoreOne: "file"})
		cut = cut || (max > 0 && len(fs) > max)
	}
	if n := len(p.Past); n > 0 {
		r.Blank()
		r.Note(view.Attention, report.Plural(int64(n), "file would be past its", "files would be past their")+
			" retention:")
		list(p.Past)
	}
	if n := len(p.NoFileDate); n > 0 {
		r.Blank()
		r.Note(view.Attention, report.Plural(int64(n), "file has", "files have")+" no file_date to count "+
			p.To.Value+" from: "+pluralWord(n, "it would need one", "they would need one"))
		list(p.NoFileDate)
	}
	if n := len(p.LoseDate); n > 0 {
		r.Blank()
		r.Note(view.Attention, report.Plural(int64(n), "file would lose its", "files would lose their")+
			" keep_until: "+p.To.Name+" has no value yet, so "+pluralWord(n, "it", "they")+" would need a retention")
		list(p.LoseDate)
	}
	return cut
}

// askChange shows what a change of policy would do and asks question, offering l when a list was cut.
func (e *env) askChange(ctx context.Context, p store.PolicyPreview, question string, yes bool) (bool, error) {
	var relist func()
	if e.showPreview(p, e.view.Overview()) {
		relist = func() { e.showPreview(p, 0); e.view.Blank() }
	}
	return e.confirmChange(ctx, question, yes, relist)
}

func (e *env) policySet(ctx context.Context, s *store.Store, name, value string, yes bool) error {
	p, err := s.PreviewPolicy(ctx, name, value, today())
	if err != nil {
		return err
	}
	r := e.view
	if p.From.Value == value {
		r.Outcome("Unchanged", p.From.Name+" is "+value+" already")
		return nil
	}
	if needsAsking(p) {
		verb, files := "Change", report.Plural(int64(p.Files), "file", "files")
		question := fmt.Sprintf("Change %s for %s, leaving %s without a file_date?", p.From.Name, files,
			report.Count(int64(len(p.NoFileDate))))
		if len(p.Past) > 0 {
			verb = "Shorten"
			question = fmt.Sprintf("Shorten %s for %s, putting %s past %s retention", p.From.Name, files,
				report.Count(int64(len(p.Past))), pluralWord(len(p.Past), "its", "their"))
			if n := len(p.NoFileDate); n > 0 {
				question += fmt.Sprintf(" and leaving %s without a file_date", report.Count(int64(n)))
			}
			question += "?"
		}
		r.Heading(verb+" "+p.From.Name, view.Where{Roots: previewRoots(p)}, valueWord(p.From.Value)+" -> "+value,
			files)
		ok, err := e.askChange(ctx, p, question, yes)
		if err != nil || !ok {
			return err
		}
	}
	n, err := s.SetPolicy(ctx, name, value, now())
	if err != nil {
		return err
	}
	facts := []string{p.From.Name + " is " + value, report.Plural(n, "file", "files") + " updated"}
	if len(p.Past) > 0 {
		facts = append(facts, report.Count(int64(len(p.Past)))+" past "+pluralWord(len(p.Past), "its", "their")+
			" retention")
	}
	if len(p.NoFileDate) > 0 {
		facts = append(facts, report.Plural(int64(len(p.NoFileDate)), "needs a file_date", "need a file_date"))
	}
	r.Outcome("Done", facts...)
	if len(p.Past) > 0 {
		r.Blank()
		r.Next("Nothing has been deleted. Next: filo delete")
	}
	return nil
}

// previewRoots are the roots of the files a preview lists, so the heading names them and paths show relative.
func previewRoots(p store.PolicyPreview) []string {
	var roots []string
	for _, fs := range [][]store.PolicyFile{p.Past, p.NoFileDate, p.LoseDate} {
		for _, f := range fs {
			roots = append(roots, f.RootPath)
		}
	}
	return roots
}

func (e *env) policyMove(ctx context.Context, s *store.Store, from, to string, yes bool) error {
	p, err := s.PreviewMove(ctx, from, to, today())
	if err != nil {
		return err
	}
	if p.To.ID == p.From.ID {
		return fmt.Errorf("%s and %s are the same policy (names are compared in any case)", from, to)
	}
	r := e.view
	rename := p.To.ID == 0
	files := report.Plural(int64(p.Files), "file", "files")
	var ok bool
	if rename {
		r.Heading("Rename "+p.From.Name, view.Where{}, "to "+p.To.Name, files)
		ok, err = e.confirmChange(ctx, fmt.Sprintf("Rename %s to %s (%s)?", p.From.Name, p.To.Name, files), yes, nil)
	} else {
		r.Heading("Move "+p.From.Name, view.Where{Roots: previewRoots(p)},
			"to "+p.To.Name+" ("+valueWord(p.To.Value)+")", files)
		ok, err = e.askChange(ctx, p, fmt.Sprintf("Move %s from %s to %s (%s)?", files, p.From.Name, p.To.Name,
			valueWord(p.To.Value)), yes)
	}
	if err != nil || !ok {
		return err
	}
	n, err := s.MovePolicy(ctx, from, to, now())
	if err != nil {
		return err
	}
	if rename {
		r.Outcome("Done", "renamed "+p.From.Name+" to "+p.To.Name)
		return nil
	}
	r.Outcome("Done", "moved "+report.Plural(n, "file", "files")+" to "+p.To.Name)
	r.Blank()
	r.Next("Next: filo policies remove " + p.From.Name + ", now it has no files")
	return nil
}

func (e *env) policyRemove(ctx context.Context, s *store.Store, name string) error {
	err := s.RemovePolicy(ctx, name, now())
	if errors.Is(err, store.ErrPolicyInUse) {
		p, perr := s.Policy(ctx, name)
		if perr != nil {
			return perr
		}
		return fmt.Errorf("%s is used by %s: move %s first (filo policies move %s NAME)", p.Name,
			report.Plural(p.Files, "file", "files"), pluralWord(int(p.Files), "it", "them"), p.Name)
	}
	if err != nil {
		return err
	}
	n, _ := store.PolicyName(name)
	e.view.Outcome("Removed", n)
	return nil
}

func (e *env) policyShow(ctx context.Context, s *store.Store, name string) error {
	p, err := s.Policy(ctx, name)
	if err != nil {
		return err
	}
	files, err := s.PolicyFiles(ctx, name, today())
	if err != nil {
		return err
	}
	// The files that need a file_date first: they are what needs doing.
	var rows, needs []view.FileRow
	var roots []string
	for _, f := range files {
		roots = append(roots, f.RootPath)
		row := view.FileRow{Root: f.RootPath, Rel: f.Path, Cells: []string{f.FileDate, f.KeepUntil}}
		if f.NeedsFileDate() {
			row.Cells[1] = "needs a file_date"
			needs = append(needs, row)
			continue
		}
		rows = append(rows, row)
	}
	rows = append(needs, rows...)
	r := e.view
	r.Heading("Policy "+p.Name, view.Where{Roots: roots}, policyWhat(p.Value), report.Plural(p.Files, "file", "files"))
	if len(rows) > 0 {
		r.Blank()
		r.Files(view.FileList{Columns: []view.Column{{Header: "file_date"}, {Header: "until"}}, Rows: rows,
			Max: r.Overview(), More: "files", MoreOne: "file"})
	}
	r.Blank()
	switch {
	case len(needs) > 0:
		r.Next("Next: filo export DIR -o FILE, fill in file_date, then filo update FILE")
	case p.Value == "":
		r.Next("Next: filo policies set " + p.Name + " VALUE to give it one")
	default:
		r.Next("Next: filo policies set " + p.Name + " VALUE to change it for all of them")
	}
	return nil
}
