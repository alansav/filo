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

// policyWhat words a policy's value: "7y from each file's file_date", "forever", "2030-12-31", "no value yet".
func policyWhat(value string) string {
	switch {
	case value == "":
		return "no value yet"
	case store.IsPeriod(value):
		return value + " from each file's file_date"
	}
	return value
}

// policyAttention is what needs attention about a policy, "" if nothing: no value, a date passed, files with no
// file_date under a period.
func policyAttention(p store.Policy, today string) string {
	switch {
	case p.Value == "":
		return "needs a retention"
	case p.Value != store.Forever && !store.IsPeriod(p.Value) && p.Value <= today && p.Files == 0:
		return "passed: set a new date, or remove it"
	case p.Value != store.Forever && !store.IsPeriod(p.Value) && p.Value <= today:
		return "passed: its files are past their retention (filo delete)"
	case p.NoFileDate > 0:
		return report.Count(p.NoFileDate) + " without a file_date"
	}
	return ""
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
		r.Next("Next: filo policies add NAME VALUE, or type a policy name in a retention CSV")
		return nil
	}
	rows := make([]view.TableRow, len(ps))
	attention := int64(0)
	first := ""
	for i, p := range ps {
		value, detail := p.Value, ""
		switch {
		case value == "":
			value = "no value"
		case store.IsPeriod(value):
			detail = "from each file's file_date"
		}
		rows[i] = view.TableRow{Name: p.Name, Cells: []string{value, detail, report.Plural(p.Files, "file", "files")},
			Attention: policyAttention(p, today())}
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
	what := value
	if what == "" {
		what = "no value yet"
	}
	e.view.Outcome("Added", n, what)
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

// showPreview writes what a change of policy would do that needs a question: the files it puts past their date, and
// those it leaves without a file_date to count from. It reports whether there is anything to ask about.
func (e *env) showPreview(p store.PolicyPreview) bool {
	r := e.view
	cols := []view.Column{{Header: "file_date"}, {Header: "until"}}
	if n := len(p.Past); n > 0 {
		r.Blank()
		r.Note(view.Attention, report.Plural(int64(n), "file would be past its", "files would be past their")+
			" retention:")
		r.Files(view.FileList{Columns: cols, Rows: policyRows(p.Past), Max: r.Overview(), More: "files",
			MoreOne: "file"})
	}
	if n := len(p.NoFileDate); n > 0 {
		r.Blank()
		r.Note(view.Attention, report.Plural(int64(n), "file has", "files have")+" no file_date to count "+
			p.To.Value+" from: "+pick(n == 1, "it would need one", "they would need one"))
		r.Files(view.FileList{Columns: cols, Rows: policyRows(p.NoFileDate), Max: r.Overview(),
			More: "files", MoreOne: "file"})
	}
	return len(p.Past) > 0 || len(p.NoFileDate) > 0
}

// confirm asks question, unless yes says to go ahead. Without a terminal nothing is done, and one line says so. It
// reports whether to go ahead.
func (e *env) confirm(ctx context.Context, question string, yes bool) (bool, error) {
	r := e.view
	if yes {
		return true, nil
	}
	r.Blank()
	if !e.canPrompt() {
		r.Next("Nothing changed: there is no terminal to ask in. Run it in a terminal, or pass -y.")
		return false, nil
	}
	pr := newPrompter(e.stdin)
	pr.end, pr.check = r.Blank, e.outputErr
	defer pr.close()
	key, err := e.askChoice(ctx, pr, question, []view.Choice{{Key: "y", Word: "yes"}, {Key: "n", Word: "no"}}, "n")
	if err != nil {
		return false, interrupted(err)
	}
	r.Blank()
	if key != "y" {
		r.Next("Nothing changed.")
		return false, nil
	}
	return true, nil
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
	from := p.From.Value
	if from == "" {
		from = "no value"
	}
	if e.showPreviewHeading("Change", p, from+" -> "+value) {
		var question string
		switch {
		case len(p.Past) > 0:
			question = fmt.Sprintf("Shorten %s for %s, putting %s past %s retention?", p.From.Name,
				report.Plural(int64(p.Files), "file", "files"), report.Count(int64(len(p.Past))),
				pick(len(p.Past) == 1, "its", "their"))
		default:
			question = fmt.Sprintf("Change %s for %s, leaving %s without a file_date?", p.From.Name,
				report.Plural(int64(p.Files), "file", "files"), report.Count(int64(len(p.NoFileDate))))
		}
		ok, err := e.confirm(ctx, question, yes)
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
		facts = append(facts, report.Count(int64(len(p.Past)))+" past "+pick(len(p.Past) == 1, "its", "their")+
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

// showPreviewHeading writes a change's heading and, when it needs a question, what it would do; it reports whether
// it needs one. The heading is only written then: a change with nothing to ask about is one Done line.
func (e *env) showPreviewHeading(verb string, p store.PolicyPreview, change string) bool {
	if len(p.Past) == 0 && len(p.NoFileDate) == 0 {
		return false
	}
	if len(p.Past) > 0 && verb == "Change" {
		verb = "Shorten"
	}
	e.view.Heading(verb+" "+p.From.Name, view.Where{Roots: previewRoots(p)}, change,
		report.Plural(int64(p.Files), "file", "files"))
	return e.showPreview(p)
}

// previewRoots are the roots of the files a preview lists, so the heading names them and paths show relative.
func previewRoots(p store.PolicyPreview) []string {
	var roots []string
	for _, f := range append(append([]store.PolicyFile{}, p.Past...), p.NoFileDate...) {
		roots = append(roots, f.RootPath)
	}
	return roots
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func (e *env) policyMove(ctx context.Context, s *store.Store, from, to string, yes bool) error {
	p, err := s.PreviewMove(ctx, from, to, today())
	if err != nil {
		return err
	}
	r := e.view
	rename := p.To.ID == 0
	files := report.Plural(int64(p.Files), "file", "files")
	var question string
	if rename {
		question = fmt.Sprintf("Rename %s to %s (%s)?", p.From.Name, p.To.Name, files)
		r.Heading("Rename "+p.From.Name, view.Where{}, "to "+p.To.Name, files)
	} else {
		question = fmt.Sprintf("Move %s from %s to %s (%s)?", files, p.From.Name, p.To.Name, valueWord(p.To.Value))
		r.Heading("Move "+p.From.Name, view.Where{Roots: previewRoots(p)}, "to "+p.To.Name+" ("+valueWord(p.To.Value)+")",
			files)
		e.showPreview(p)
	}
	ok, err := e.confirm(ctx, question, yes)
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

func valueWord(v string) string {
	if v == "" {
		return "no value"
	}
	return v
}

func (e *env) policyRemove(ctx context.Context, s *store.Store, name string) error {
	err := s.RemovePolicy(ctx, name, now())
	if errors.Is(err, store.ErrPolicyInUse) {
		p, perr := s.Policy(ctx, name)
		if perr != nil {
			return perr
		}
		return fmt.Errorf("%s is used by %s: move %s first (filo policies move %s NAME)", p.Name,
			report.Plural(p.Files, "file", "files"), pick(p.Files == 1, "it", "them"), p.Name)
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
		r.Next("Next: filo export retention DIR -o FILE, fill in file_date, then filo update FILE")
	case p.Value == "":
		r.Next("Next: filo policies set " + p.Name + " VALUE to give it one")
	default:
		r.Next("Next: filo policies set " + p.Name + " VALUE to change it for all of them")
	}
	return nil
}
