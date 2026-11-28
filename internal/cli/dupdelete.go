package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/alansav/filo/internal/dispose"
	"github.com/alansav/filo/internal/duplicates"
	"github.com/alansav/filo/internal/fsid"
	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// jobState is what filo delete FILE.csv does with a group of the CSV.
type jobState int

const (
	jobDelete  jobState = iota // delete copies (and maybe store the copies kept as records)
	jobRecord                  // nothing to delete: store a content always or a set of records
	jobDone                    // nothing left to do: done by an earlier run, or removed outside filo
	jobSkipped                 // the files are not as exported, or cannot be checked: left alone
	jobRefused                 // the CSV's rows or the retention rule refuse it: left alone (exit 1)
	jobBlank                   // not decided
)

// dupJob is one group of the CSV, checked against the disk (docs/design.md, Duplicates, filo delete FILE.csv, step 2).
type dupJob struct {
	key       duplicates.GroupKey
	line      int    // its first CSV line
	name      string // the content's file name, from its first copy
	size      int64
	hash      string // the content exported
	state     jobState
	reason    string
	root, rel string // the copy the reason is about, if any
	always    bool
	keep, del []store.ExportedCopy // present on disk as exported
	gone      []goneCopy           // copies gone since the export: reported and ignored
	records   bool                 // the copies kept are to be stored as a set of records
	roots     []string             // the roots of its copies
}

type goneCopy struct {
	store.ExportedCopy
	why string // "deleted by filo" or "removed outside filo"
}

// deleteDuplicates carries out a duplicates CSV (docs/design.md, Duplicates, filo delete FILE.csv): it reads it,
// checks each decided group against the disk, lists what it will and will not do, asks, and carries it out a group at
// a time, each with the check at the moment of deleting.
func (e *env) deleteDuplicates(ctx context.Context, s *store.Store, csvPath string, yes bool) error {
	data, err := sheet.ReadFile(csvPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", csvPath, err)
	}
	parsed, err := duplicates.ParseCSV(data)
	if err != nil {
		return fmt.Errorf("%s: %w", csvPath, err)
	}
	var ids []int64
	for _, r := range parsed.Rows {
		if !slices.Contains(ids, r.Key.Export) {
			ids = append(ids, r.Key.Export)
		}
	}
	slices.Sort(ids)
	exported, err := s.ExportedGroups(ctx, ids)
	if err != nil {
		return err
	}
	plans, problems := duplicates.Plan(parsed, exported)
	copies, err := s.ExportedCopies(ctx, ids)
	if err != nil {
		return err
	}
	roots, err := s.Roots(ctx)
	if err != nil {
		return err
	}
	jobs := checkGroups(plans, copies, rootChecker(roots))
	links, err := symlinksTo(ctx, s, jobs)
	if err != nil {
		return err
	}

	r := e.view
	l := &dupList{csv: filepath.Base(csvPath), exports: ids, jobs: jobs, problems: problems, links: links}
	asking := !yes && e.canPrompt()
	max := 0
	if asking {
		max = r.Overview()
	}
	cut := l.draw(r, max)
	refused := l.count(jobRefused) > 0 || len(problems) > 0
	nDel, nRec := l.toDo()
	// Nothing done: still exit 1 if the CSV refused anything, however the run ends.
	nothing := func(line string) error {
		r.Next(line)
		if refused {
			return errReported
		}
		return nil
	}
	if nDel == 0 && nRec == 0 {
		return nothing("Nothing to do.")
	}

	selecting := false
	var pr *prompter
	if !yes {
		if !asking {
			return nothing("Nothing deleted: there is no terminal to ask in (filo asks only when both the question " +
				"and your answer are in a terminal). Run filo delete in a terminal, or pass -y to do all of it.")
		}
		pr = newPrompter(e.stdin)
		pr.end, pr.check = r.Blank, e.outputErr
		defer pr.close()
		var parts []string
		if nDel > 0 {
			parts = append(parts, "Delete "+report.Plural(int64(nDel), "copy", "copies"))
		}
		if nRec > 0 {
			rec := report.Plural(int64(nRec), "decision", "decisions")
			if nDel > 0 {
				parts = append(parts, "record "+rec)
			} else {
				parts = append(parts, "Record "+rec)
			}
		}
		question := strings.Join(parts, " and ") + "?"
		choices := []view.Choice{{Key: "y", Word: "yes"}, {Key: "s", Word: "select"}, {Key: "l", Word: "list"},
			{Key: "n", Word: "no"}}
	ask:
		for {
			key, err := e.askChoice(ctx, pr, question, choices, "n")
			if err != nil {
				return interrupted(err)
			}
			switch key {
			case "y":
				if !cut {
					break ask
				}
				// Everything deleted is on screen right before the last question (docs/design.md, Terminal output).
				r.Blank()
				l.draw(r, 0)
				sure, err := e.askChoice(ctx, pr, question, []view.Choice{{Key: "y", Word: "yes"},
					{Key: "n", Word: "no"}}, "n")
				if err != nil {
					return interrupted(err)
				}
				if sure != "y" {
					return nothing("Nothing deleted.")
				}
				break ask
			case "s":
				selecting = true
				break ask
			case "l":
				r.Blank()
				cut = l.draw(r, 0)
			default:
				return nothing("Nothing deleted.")
			}
		}
		r.Blank()
	}

	run := s.BeginDeleteRun(now())
	d := dispose.New(run, now)
	var (
		deleted, recorded, left int
		freed                   int64
		skips                   []view.Problem
		failed                  bool
		runErr                  error
	)
	var todo []*dupJob
	for _, j := range jobs {
		if j.state == jobDelete || j.state == jobRecord {
			todo = append(todo, j)
		}
	}
	choices := []view.Choice{{Key: "d", Word: "do", Label: "do it"}, {Key: "s", Word: "skip", Label: "skip"},
		{Key: "q", Word: "quit", Label: "quit"}}
groups:
	for i, j := range todo {
		if selecting {
			if i > 0 {
				r.Blank()
			}
			r.Item(i+1, len(todo), j.name, report.Size(j.size)+" each")
			for _, c := range j.keep {
				r.ItemPath("keep", c.RootPath, c.Path, keepNote(c.Candidate))
			}
			for _, c := range j.del {
				r.ItemPath("delete", c.RootPath, c.Path, keepNote(c.Candidate))
			}
			if j.always {
				r.ItemPath("always", j.keep[0].RootPath, j.keep[0].Path, report.Plural(int64(len(j.keep)), "copy",
					"copies"))
			}
			key := ""
			for key == "" {
				ans, ok, err := pr.ask(ctx, func() { r.Choose(choices) })
				if err != nil {
					runErr = err
					left += len(todo) - i
					break groups
				}
				if !ok {
					key = "q"
					break
				}
				k, known := view.Pick(ans, choices, "")
				if !known || k == "" {
					r.Again(choices)
					continue
				}
				key = k
			}
			switch key {
			case "s":
				left++
				r.Result(view.Neutral, "skipped")
				continue
			case "q":
				left += len(todo) - i
				break groups
			}
		}
		if err := e.outputErr(); err != nil { // never act once output has failed: what is done must have been shown
			runErr = err
			left += len(todo) - i
			break
		}
		res, err := carryOut(ctx, s, d, j)
		deleted += res.deleted
		freed += res.freed
		if res.recorded {
			recorded++
		}
		for _, sk := range res.skips {
			skips = append(skips, view.Problem{Group: int(j.key.Group), Root: sk.root, Rel: sk.rel, Reason: sk.reason,
				Failed: sk.failed})
			failed = failed || sk.failed
		}
		if selecting && err == nil {
			res.show(r)
		}
		if err != nil {
			runErr = err
			left += len(todo) - i - 1
			break
		}
	}
	removed, prob := d.PruneDirs()
	finishErr := run.Finish(context.WithoutCancel(ctx), now())

	if selecting {
		r.Blank()
	}
	facts := []string{"deleted " + report.Plural(int64(deleted), "copy", "copies"), "freed " + report.Size(freed)}
	if recorded > 0 {
		facts = append(facts, "recorded "+report.Plural(int64(recorded), "decision", "decisions"))
	}
	if len(removed) > 0 {
		facts = append(facts, "removed "+report.Plural(int64(len(removed)), "empty directory", "empty directories"))
	}
	r.Outcome("Done", facts...)
	if len(skips) > 0 {
		r.Blank()
		r.Problems(fmt.Sprintf("Skipped %s: nothing was changed", report.Count(int64(len(skips)))), view.Attention,
			skips)
	}
	if len(prob) > 0 {
		ps := make([]view.Problem, len(prob))
		for i, p := range prob {
			ps[i] = view.Problem{Reason: p.Error(), Failed: true}
		}
		r.Blank()
		r.Problems(fmt.Sprintf("Directories not removed: %s", report.Count(int64(len(prob)))), view.Attention, ps)
	}
	if left > 0 {
		r.Blank()
		r.Count(view.Neutral, report.Plural(int64(left), "group", "groups")+" left for later")
	}
	switch {
	case runErr != nil:
		return interrupted(runErr)
	case finishErr != nil:
		return finishErr
	case failed || len(prob) > 0 || refused:
		return errReported
	}
	return nil
}

// rootChecker returns a function that says why a root cannot be checked now ("" if it can), looking each root up
// once: a disk not mounted must never make its files look gone.
func rootChecker(roots []store.Root) func(id int64) string {
	byID := map[int64]store.Root{}
	for _, r := range roots {
		byID[r.ID] = r
	}
	seen := map[int64]string{}
	return func(id int64) string {
		if why, ok := seen[id]; ok {
			return why
		}
		why := ""
		if r, ok := byID[id]; !ok {
			why = "its root is no longer in the register"
		} else if err := store.CheckRoot(r); err != nil {
			why = store.ErrDetail(err) + "; is the drive mounted?"
		}
		seen[id] = why
		return why
	}
}

// copyState is what the check against the disk found for one copy.
type copyState int

const (
	copyThere copyState = iota // at its path, as exported
	copyGone                   // not there: filo deleted it, or it was removed outside filo
	copyNot                    // changed, moved, or cannot be checked: its group is skipped
)

// checkCopy checks a copy against the disk and the register, without reading its content (that is done at the moment
// of deleting): it must be at its exported path, the same file, with the size and modification time the register
// has for the content exported. A file whose directory can be read and that is not in it is gone; one that cannot be
// checked never is.
func checkCopy(c store.ExportedCopy, rootWhy func(int64) string) (copyState, string) {
	if c.Deleted != "" {
		return copyGone, "deleted by filo"
	}
	if why := rootWhy(c.RootID); why != "" {
		return copyNot, "can't check it: " + why
	}
	fi, err := os.Lstat(c.AbsPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if c.OtherPath != "" {
			return copyNot, "moved since the export (now " + c.OtherPath + "): export again"
		}
		return copyGone, "removed outside filo"
	case err != nil:
		return copyNot, "can't check it: " + store.ErrDetail(err)
	case c.OtherPath != "" && !c.PathOpen:
		return copyNot, "moved since the export (now " + c.OtherPath + "): export again"
	case !c.PathOpen:
		return copyNot, "back at its path since the last scan: run filo scan, then export again"
	case !fi.Mode().IsRegular():
		return copyNot, "no longer a regular file: export again"
	case c.Unreadable:
		return copyNot, "the last scan could not read it: run filo scan, then export again"
	case c.Hash != c.ReviewedHash:
		return copyNot, "changed since the export: export again"
	}
	if id, ok := fsid.FromFileInfo(fi); ok && c.Ino != 0 && id.Ino != c.Ino {
		return copyNot, "replaced by another file since the export: run filo scan, then export again"
	}
	if fi.Size() != c.Size || fi.ModTime().UnixNano() != c.MtimeNS {
		return copyNot, "changed since the export: export again"
	}
	return copyThere, ""
}

// checkGroups turns each group's plan into what will be done with it, checking every decided group against the disk
// (step 2): a group any of whose copies changed, moved or cannot be checked is skipped whole; copies gone are
// reported and ignored; then the group rules on what is left, and the retention rule. A group of two overlapping
// exports is carried out once (see once).
func checkGroups(plans []duplicates.GroupPlan, copies map[int64][]store.ExportedCopy,
	rootWhy func(int64) string) []*dupJob {
	byGroup := map[duplicates.GroupKey][]store.ExportedCopy{}
	for ex, cs := range copies {
		for _, c := range cs {
			k := duplicates.GroupKey{Export: ex, Group: c.Group}
			byGroup[k] = append(byGroup[k], c)
		}
	}
	var jobs []*dupJob
	for _, p := range plans {
		j := &dupJob{key: p.Key}
		if len(p.Lines) > 0 {
			j.line = p.Lines[0]
		}
		group := byGroup[p.Key]
		for _, c := range group {
			j.roots = append(j.roots, c.RootPath)
		}
		if len(group) > 0 {
			j.name, j.size, j.hash = path.Base(group[0].Path), group[0].Size, group[0].ReviewedHash
		}
		jobs = append(jobs, j)
		switch p.Status {
		case duplicates.NotDecided:
			j.state = jobBlank
			continue
		case duplicates.Refused:
			j.state, j.reason = jobRefused, p.Reason
			continue
		}
		if len(group) == 0 {
			j.state, j.reason = jobRefused, "not in its export: export again"
			continue
		}
		decide(j, p, group, rootWhy)
	}
	once(jobs)
	return jobs
}

// once makes each copy deleted, each set of records stored and each content made always by one group only. Two
// overlapping exports in one CSV hold the same group twice; the plan refuses them if their actions differ, and with
// the same actions the later group does only what the earlier one does not already do.
func once(jobs []*dupJob) {
	deleting := map[int64]duplicates.GroupKey{}  // file -> the group deleting it
	recording := map[int64]duplicates.GroupKey{} // file -> the group storing it as a record
	always := map[string]duplicates.GroupKey{}   // content -> the group making it always
	where := func(k duplicates.GroupKey) string {
		return "group " + strconv.FormatInt(k.Group, 10) + " of export #" + strconv.FormatInt(k.Export, 10)
	}
	for _, j := range jobs {
		if j.state != jobDelete && j.state != jobRecord {
			continue
		}
		if j.always {
			if k, ok := always[j.hash]; ok {
				j.state, j.reason = jobDone, "the same content as "+where(k)+", which makes it always"
				continue
			}
			always[j.hash] = j.key
			continue
		}
		var del []store.ExportedCopy
		var earlier duplicates.GroupKey
		for _, c := range j.del {
			if k, ok := deleting[c.FileID]; ok {
				earlier = k
				continue
			}
			deleting[c.FileID] = j.key
			del = append(del, c)
		}
		if j.records {
			k, ok := recording[j.keep[0].FileID]
			same := ok
			for _, c := range j.keep {
				same = same && recording[c.FileID] == k
			}
			if same {
				j.records, earlier = false, k
			} else {
				for _, c := range j.keep {
					recording[c.FileID] = j.key
				}
			}
		}
		j.del = del
		switch {
		case len(del) > 0:
		case j.records:
			j.state = jobRecord
		default:
			j.state, j.reason = jobDone, "the same as "+where(earlier)+", which does it"
		}
	}
}

// decide checks a decided group's copies against the disk and works out what is left to do.
func decide(j *dupJob, p duplicates.GroupPlan, group []store.ExportedCopy, rootWhy func(int64) string) {
	skip := func(c store.ExportedCopy, why string) {
		j.state, j.reason, j.root, j.rel = jobSkipped, why, c.RootPath, c.Path
	}
	set := func(ids []int64) map[int64]bool {
		m := make(map[int64]bool, len(ids))
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	toKeep, toDelete, missing := set(p.Keep), set(p.Delete), set(p.Missing)
	var keep, del []store.ExportedCopy
	for _, c := range group {
		st, why := checkCopy(c, rootWhy)
		switch {
		case st == copyNot:
			skip(c, why)
			return
		case st == copyGone:
			j.gone = append(j.gone, goneCopy{c, why})
		case missing[c.FileID]:
			skip(c, "its row was deleted from the CSV, but the file is still there: put the row back, or export again")
			return
		case p.Always, toKeep[c.FileID]:
			keep = append(keep, c)
		case toDelete[c.FileID]:
			del = append(del, c)
		}
	}
	j.keep, j.del = keep, del
	goneWhy := func(only map[int64]bool) string {
		var whys []string
		for _, g := range j.gone {
			if (only == nil || only[g.FileID]) && !slices.Contains(whys, g.why) {
				whys = append(whys, g.why)
			}
		}
		return strings.Join(whys, ", ")
	}
	switch {
	case p.Always:
		j.always = true
		switch {
		case len(keep) == 0:
			j.state, j.reason = jobDone, "every copy is gone ("+goneWhy(nil)+")"
		case keep[0].Always:
			j.state, j.reason = jobDone, "already done: the content is marked always"
		default:
			j.state = jobRecord
		}
		return
	case len(keep) == 0 && len(del) == 0:
		j.state, j.reason = jobDone, "every copy is gone ("+goneWhy(nil)+")"
		return
	case len(keep) == 0:
		g := j.gone[0]
		for _, c := range j.gone {
			if toKeep[c.FileID] {
				g = c
				break
			}
		}
		skip(g.ExportedCopy, "no copy would be left: the copy to keep is gone ("+g.why+")")
		return
	}
	// A copy on a period policy with no file_date has a retention whose end is not known yet, which no copy kept can
	// be shown to outlast: it is not deleted until it has one (docs/design.md, Named retention policies). Likewise a
	// copy on a policy with no value yet: its retention's length is not decided.
	for _, c := range del {
		switch {
		case c.NeedsFileDate():
			j.state, j.reason, j.root, j.rel = jobRefused, "needs a file_date first: its policy "+c.Policy+" ("+
				c.PolicyValue+") counts from it", c.RootPath, c.Path
			return
		case c.Policy != "" && c.PolicyValue == "":
			j.state, j.reason, j.root, j.rel = jobRefused, "its policy "+c.Policy+" has no value yet: give it one "+
				"first (filo policies set "+c.Policy+" VALUE)", c.RootPath, c.Path
			return
		}
	}
	// The retention rule: a copy can be deleted only if a kept copy lasts at least as long.
	best := keep[0]
	for _, c := range keep[1:] {
		if !store.LastsAtLeast(best.KeepUntil, c.KeepUntil) {
			best = c
		}
	}
	for _, c := range del {
		if !store.LastsAtLeast(best.KeepUntil, c.KeepUntil) {
			j.state, j.reason, j.root, j.rel = jobRefused, "lasts longer ("+keepNote(c.Candidate)+") than every "+
				"copy kept: keep it instead, or keep both", c.RootPath, c.Path
			return
		}
	}
	j.records = len(keep) >= 2 && !oneSet(keep)
	switch {
	case len(del) > 0:
		j.state = jobDelete
	case j.records:
		j.state = jobRecord
	case len(keep) < len(p.Keep):
		// Records asked for, but one copy to keep is left: a set of one keeps nothing out.
		j.state, j.reason = jobDone, "one copy to keep is left, the others gone ("+goneWhy(toKeep)+
			"): nothing to record"
	case len(j.gone) == 0:
		j.state, j.reason = jobDone, "already done: the "+strconv.Itoa(len(keep))+" copies are kept as separate files"
	default:
		j.state, j.reason = jobDone, "already done: the copies to delete are gone ("+goneWhy(toDelete)+")"
	}
	if j.state != jobDelete {
		return
	}
	// Only when there is something to delete: a copy to keep must be readable, or it cannot be the copy kept (D4);
	// a copy to delete must be the only link to its file, or deleting it frees nothing.
	readable := false
	var unreadable store.ExportedCopy
	var why string
	for _, c := range keep {
		f, err := os.Open(c.AbsPath())
		if err == nil {
			f.Close()
			readable = true
			break
		}
		unreadable, why = c, store.ErrDetail(err)
	}
	if !readable {
		skip(unreadable, "cannot be read, so it cannot be the copy kept: "+why)
		return
	}
	for _, c := range del {
		if why, ok := dispose.OnlyLink(c.AbsPath()); !ok {
			skip(c, why)
			return
		}
	}
}

// oneSet reports whether the copies are already records of one set.
func oneSet(cs []store.ExportedCopy) bool {
	for _, c := range cs {
		if c.RecordSet == 0 || c.RecordSet != cs[0].RecordSet {
			return false
		}
	}
	return true
}

// keepNote is a copy's retention as shown after its path: "until 2027-12-31", "forever", or nothing.
func keepNote(c store.Candidate) string {
	switch c.KeepUntil {
	case "":
		return ""
	case store.Forever:
		return "forever"
	}
	return "until " + c.KeepUntil
}

// symlinksTo finds the symlinks in the register that point at a copy to delete: they would break.
func symlinksTo(ctx context.Context, s *store.Store, jobs []*dupJob) (map[string][]string, error) {
	var targets []string
	for _, j := range jobs {
		if j.state == jobDelete {
			for _, c := range j.del {
				targets = append(targets, c.AbsPath())
			}
		}
	}
	return s.SymlinksTo(ctx, targets)
}

// dupList is what filo delete FILE.csv will and will not do, drawn as an overview, and again in full for l.
type dupList struct {
	csv      string
	exports  []int64
	jobs     []*dupJob
	problems []sheet.Problem
	links    map[string][]string
}

func (l *dupList) count(st jobState) int {
	n := 0
	for _, j := range l.jobs {
		if j.state == st {
			n++
		}
	}
	return n
}

// toDo counts the copies to delete and the decisions to record.
func (l *dupList) toDo() (del, rec int) {
	for _, j := range l.jobs {
		switch {
		case j.state == jobDelete:
			del += len(j.del)
			if j.records {
				rec++
			}
		case j.state == jobRecord:
			rec++
		}
	}
	return del, rec
}

// draw writes the list (docs/design.md, Terminal output: "Will delete", "Will record", "Won't touch"). max > 0 shows
// that many groups to delete and "… N more groups"; it reports whether any were left out.
func (l *dupList) draw(r *view.Renderer, max int) (cut bool) {
	var roots []string
	for _, j := range l.jobs {
		roots = append(roots, j.roots...)
	}
	ex := "export "
	if len(l.exports) > 1 {
		ex = "exports "
	}
	var exs []string
	for _, id := range l.exports {
		exs = append(exs, "#"+strconv.FormatInt(id, 10))
	}
	r.Heading("Duplicates", view.Where{Roots: roots}, l.csv, ex+strings.Join(exs, ", "),
		report.Plural(int64(len(l.jobs)), "group", "groups"))

	var groups []view.DupGroup
	var delGroups, delCopies int
	var bytes int64
	for _, j := range l.jobs {
		if j.state != jobDelete {
			continue
		}
		g := view.DupGroup{Name: j.name, Each: report.Size(j.size)}
		for _, c := range j.keep {
			g.Keep = append(g.Keep, view.Copy{Root: c.RootPath, Rel: c.Path, Note: keepNote(c.Candidate)})
		}
		for _, c := range j.del {
			g.Delete = append(g.Delete, view.Copy{Root: c.RootPath, Rel: c.Path, Note: keepNote(c.Candidate),
				Symlinks: l.links[c.AbsPath()]})
			bytes += c.Size
		}
		delGroups++
		delCopies += len(j.del)
		groups = append(groups, g)
	}
	if len(groups) > 0 {
		r.Blank()
		r.Section("Will delete", report.Plural(int64(delCopies), "copy", "copies")+" in "+
			report.Plural(int64(delGroups), "group", "groups"), report.Size(bytes))
		r.Dups(groups, max)
	}

	var records []string
	for _, j := range l.jobs {
		switch {
		case j.state == jobRecord && j.always:
			records = append(records, "content marked always (never a duplicate): "+j.name+" ("+
				report.Plural(int64(len(j.keep)), "copy", "copies")+")")
		case (j.state == jobRecord || j.state == jobDelete) && j.records:
			records = append(records, "kept as separate files: "+j.name+" ("+
				report.Plural(int64(len(j.keep)), "copy", "copies")+")")
		}
	}
	if len(records) > 0 {
		r.Blank()
		r.Section("Will record")
		for _, rec := range records {
			r.Count(view.Kept, rec)
		}
	}

	var ps []view.Problem
	for _, p := range l.problems {
		ps = append(ps, view.Problem{Line: p.Line, Reason: p.Msg, Failed: true})
	}
	prefix := func(j *dupJob) string {
		if len(l.exports) > 1 {
			return "export #" + strconv.FormatInt(j.key.Export, 10) + ": "
		}
		return ""
	}
	for _, j := range l.jobs {
		if j.state != jobRefused && j.state != jobSkipped {
			continue
		}
		p := view.Problem{Group: int(j.key.Group), Line: j.line, Reason: prefix(j) + j.reason,
			Failed: j.state == jobRefused}
		if j.rel != "" {
			p.Root, p.Rel = j.root, j.rel
		} else if j.name != "" {
			p.Reason = prefix(j) + j.name + ": " + j.reason
		}
		ps = append(ps, p)
	}
	var notes []func()
	for _, j := range l.jobs {
		if j.state == jobDone {
			notes = append(notes, func() {
				r.Count(view.Neutral, "group "+strconv.FormatInt(j.key.Group, 10)+"   "+prefix(j)+j.name+": "+j.reason)
			})
		}
		if j.state == jobDelete || j.state == jobRecord {
			for _, g := range j.gone {
				notes = append(notes, func() {
					r.CountName(view.Neutral, "group "+strconv.FormatInt(j.key.Group, 10)+"   "+prefix(j),
						r.Path(g.RootPath, g.Path), ": gone ("+g.why+"); its row is ignored")
				})
			}
		}
	}
	if blank := l.count(jobBlank); blank > 0 {
		notes = append(notes, func() {
			r.Count(view.Neutral, report.Plural(int64(blank), "group", "groups")+" not decided (blank)")
		})
	}
	if len(ps) > 0 || len(notes) > 0 {
		r.Blank()
		if len(ps) > 0 {
			r.Problems("Won't touch", view.Attention, ps)
		} else {
			r.Section("Won't touch")
		}
		for _, n := range notes {
			n()
		}
	}
	r.Blank()
	return max > 0 && len(groups) > max
}

// jobResult is what carrying out one group did.
type jobResult struct {
	deleted  int
	freed    int64
	recorded bool
	skips    []jobSkip
	whole    string // the group was skipped before anything was done, with this reason
}

type jobSkip struct {
	root, rel, reason string
	failed            bool
}

// show writes the result under a select-mode item.
func (res jobResult) show(r *view.Renderer) {
	switch {
	case res.whole != "":
		r.Result(view.Attention, "skipped: "+res.whole)
		return
	case res.deleted > 0:
		r.Result(view.Done, "deleted "+report.Plural(int64(res.deleted), "copy", "copies"))
	}
	if res.recorded {
		r.Result(view.Kept, "recorded")
	}
	for _, sk := range res.skips {
		m := view.Attention
		if sk.failed {
			m = view.Refused
		}
		r.ResultFile(m, "not deleted", sk.root, sk.rel, sk.reason)
	}
}

// carryOut does one group (step 4): a content always or a set of records is stored; copies are deleted only after a
// kept copy is found on disk (never the last copy), each through the check at the moment of deleting.
func carryOut(ctx context.Context, s *store.Store, d *dispose.Disposer, j *dupJob) (jobResult, error) {
	var res jobResult
	if j.always {
		if err := s.AddAlways(ctx, j.hash, now()); err != nil {
			return res, err
		}
		res.recorded = true
		return res, nil
	}
	if j.state == jobRecord {
		return res, recordSet(ctx, s, j, &res)
	}
	kept, why, err := findKept(ctx, j)
	if err != nil {
		return res, err
	}
	if kept == nil {
		res.whole = why
		res.skips = append(res.skips, jobSkip{reason: j.name + ": " + why})
		return res, nil
	}
	// The retention rule, against the copy actually kept.
	for _, c := range j.del {
		if !store.LastsAtLeast(kept.Candidate().KeepUntil, c.KeepUntil) {
			res.whole = "the copy kept, " + kept.Path() + ", does not last as long (" + keepNote(c.Candidate) + ")"
			res.skips = append(res.skips, jobSkip{root: c.RootPath, rel: c.Path, reason: res.whole})
			return res, nil
		}
	}
	for _, c := range j.del {
		o, err := d.DeleteDuplicate(ctx, c.Candidate, kept)
		if o.Deleted {
			res.deleted++
			res.freed += c.Size
		} else if err == nil {
			res.skips = append(res.skips, jobSkip{c.RootPath, c.Path, o.Reason, o.Failed})
		}
		if err != nil {
			return res, err
		}
	}
	if j.records {
		return res, recordSet(ctx, s, j, &res)
	}
	return res, nil
}

func recordSet(ctx context.Context, s *store.Store, j *dupJob, res *jobResult) error {
	var rc []store.RecordCopy
	for _, c := range j.keep {
		rc = append(rc, store.RecordCopy{FileID: c.FileID, Hash: c.ReviewedHash})
	}
	if err := s.RecordSet(ctx, rc, now()); err != nil {
		return err
	}
	res.recorded = true
	return nil
}

// findKept checks the copies to keep on disk, longest retention first, and returns the first that is still the
// content exported, or why none is. A copy that is, on disk, the same file as a copy to delete never counts.
func findKept(ctx context.Context, j *dupJob) (*dispose.Kept, string, error) {
	keep := slices.Clone(j.keep)
	slices.SortStableFunc(keep, func(a, b store.ExportedCopy) int {
		switch {
		case store.LastsAtLeast(a.KeepUntil, b.KeepUntil) && !store.LastsAtLeast(b.KeepUntil, a.KeepUntil):
			return -1
		case store.LastsAtLeast(b.KeepUntil, a.KeepUntil) && !store.LastsAtLeast(a.KeepUntil, b.KeepUntil):
			return 1
		}
		return 0
	})
	var whys []string
copies:
	for _, k := range keep {
		kept, why, err := dispose.CheckKept(ctx, k.Candidate)
		if err != nil {
			return nil, "", err
		}
		if kept == nil {
			whys = append(whys, k.AbsPath()+": "+why)
			continue
		}
		for _, c := range j.del {
			if same, err := dispose.SameFile(kept.Path(), c.AbsPath()); err == nil && same {
				whys = append(whys, k.AbsPath()+": the same file as "+c.AbsPath()+" (a hard link or a bind mount)")
				continue copies
			}
		}
		return kept, "", nil
	}
	return nil, "no copy to keep could be checked, so none is deleted (" + strings.Join(whys, "; ") + ")", nil
}
