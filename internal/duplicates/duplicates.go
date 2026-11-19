// Package duplicates reads and writes the duplicates CSV: every copy of each duplicate group, with an action cell in
// which the person writes delete, keep or always (docs/design.md, Duplicates). Reading it ends in a plan per group:
// what the person decided, or why the group is refused or not decided. Nothing here looks at the disk or changes the
// register: filo delete checks each decided group against the disk and carries it out. The register side (finding
// groups, recording exports) is in package store; what spreadsheets do to a CSV is handled in package sheet.
package duplicates

import (
	"cmp"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alansav/filo/internal/sheet"
	"github.com/alansav/filo/internal/store"
)

// Header is the CSV's columns, in order. Reading finds columns by name, so a spreadsheet may reorder them or add its
// own; it reads only id, export, group and action.
var Header = []string{"id", "export", "group", "copies", "directory", "name", "size", "modified", "file_date",
	"retention", "keep_until", "action"}

var (
	// ErrNotDuplicatesCSV means the header does not have the columns of a duplicates CSV.
	ErrNotDuplicatesCSV = errors.New("not a filo duplicates CSV: the header needs id, export, group and action columns")
	// ErrOlderCSV means a duplicates CSV from the first design, with a delete column of Y and N.
	ErrOlderCSV = errors.New("this CSV is from an older export (a delete column of Y and N): export again")
	// ErrRetentionCSV means a retention CSV, which filo update reads.
	ErrRetentionCSV = errors.New("this is a retention CSV: record it with filo update")
	// ErrNoAction means a duplicates CSV lost its action column (deleted or renamed in the spreadsheet).
	ErrNoAction = errors.New("the CSV has no action column; put it back with that name, or export again")
)

// IsDuplicatesCSV reports whether data's header names a duplicates CSV, of this design or the first one: it has a
// group column and an action or delete column, or a group column and no retention column (one whose action column was
// lost, which ReadCSV then explains). A retention CSV with a group column of the person's own stays a retention CSV.
func IsDuplicatesCSV(data []byte) bool {
	sh, _ := sheet.Open(data, "id", "export", "group")
	return sh != nil && (sh.Has("action") || sh.Has("delete") || !sh.Has("retention"))
}

// Action is what the person wrote in a row's action cell.
type Action byte

const (
	None   Action = iota // blank: no decision
	Delete               // delete this copy
	Keep                 // keep this copy (two or more: they are records)
	Always               // the content belongs everywhere (on every row of its group)
)

func (a Action) String() string { return [...]string{"", "delete", "keep", "always"}[a] }

// ParseAction reads an action cell: delete, keep or always, the full word or its first letter, any case, spaces
// around it ignored; blank is no decision. Anything else is refused, never guessed.
func ParseAction(s string) (Action, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return None, nil
	case "delete", "d":
		return Delete, nil
	case "keep", "k":
		return Keep, nil
	case "always", "a":
		return Always, nil
	}
	return None, fmt.Errorf("%q is not an action: write delete, keep or always (or d, k, a), or leave it blank", s)
}

// WriteCSV writes groups as one export, numbered from 1 in the order given, one row per copy. The action cell is
// blank, except keep on a copy that is one of a set of records: a new copy of records is exported with them (F3),
// to delete or to keep and join the set. Times are shown in loc.
func WriteCSV(w io.Writer, exportID int64, groups []store.DupContent, loc *time.Location) error {
	if _, err := io.WriteString(w, sheet.BOM); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(Header); err != nil {
		return err
	}
	ex := strconv.FormatInt(exportID, 10)
	for i, g := range groups {
		group := strconv.Itoa(i + 1)
		copies := strconv.Itoa(len(g.Copies))
		for _, c := range g.Copies {
			dir, name := path.Split(c.Path)
			directory := c.RootPath
			if dir = strings.TrimSuffix(dir, "/"); dir != "" {
				directory = c.RootPath + "/" + dir
			}
			action := ""
			if c.RecordSet != 0 {
				action = Keep.String()
			}
			rec := []string{
				strconv.FormatInt(c.FileID, 10), ex, group, copies, sheet.Cell(directory), sheet.Cell(name),
				strconv.FormatInt(c.Size, 10), time.Unix(0, c.MtimeNS).In(loc).Format("2006-01-02 15:04"),
				c.FileDate, c.Retention, c.KeepUntil, action,
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

// GroupKey names a group: its export and its number in that export.
type GroupKey struct{ Export, Group int64 }

// Row is one row of an edited duplicates CSV. Its export and group are known; a value that could not be read is in
// Bad, and refuses the row's group.
type Row struct {
	Line   int // the CSV line, for messages
	Key    GroupKey
	FileID int64 // 0 if its id could not be read
	Action Action
	Bad    string // why the row could not be read ("" if it could)
}

// Problem is a row that cannot be tied to a group (its export or group cell cannot be read).
type Problem struct {
	Line int
	Msg  string
}

// Parsed is what ReadCSV found.
type Parsed struct {
	Rows     []Row
	Problems []Problem
}

// ReadCSV reads an edited duplicates CSV, as spreadsheets save it (see package sheet). A CSV of the first design, a
// retention CSV, or one without an action column is refused whole. A row whose export or group cannot be read is a
// problem of its line; a row with another cell that cannot be read is kept, to refuse its group (see Plan).
func ReadCSV(r io.Reader) (Parsed, error) {
	data, err := sheet.ReadAll(r)
	if err != nil {
		return Parsed{}, err
	}
	return ParseCSV(data)
}

// ParseCSV is ReadCSV on a CSV already read (with sheet.ReadFile or sheet.ReadAll), so it is not copied again.
func ParseCSV(data []byte) (Parsed, error) {
	var p Parsed
	if len(data) > sheet.MaxSize {
		return p, sheet.ErrTooLarge
	}
	sh, _ := sheet.Open(data, "id", "export")
	switch {
	case sh == nil:
		return p, ErrNotDuplicatesCSV
	case !sh.Has("group") && sh.Has("retention"):
		return p, ErrRetentionCSV
	case !sh.Has("group"):
		return p, ErrNotDuplicatesCSV
	case sh.Has("delete") && !sh.Has("action"):
		return p, ErrOlderCSV
	case !sh.Has("action"):
		return p, ErrNoAction
	}
	err := sh.Each(func(row sheet.Row) {
		ex, exErr := strconv.ParseInt(row.Get("export"), 10, 64)
		group, gErr := strconv.ParseInt(row.Get("group"), 10, 64)
		switch {
		case exErr != nil || ex < 1:
			p.Problems = append(p.Problems, Problem{row.Line, fmt.Sprintf("export %q is not an export number",
				row.Get("export"))})
			return
		case gErr != nil || group < 1:
			p.Problems = append(p.Problems, Problem{row.Line, fmt.Sprintf("group %q is not a group number",
				row.Get("group"))})
			return
		}
		r := Row{Line: row.Line, Key: GroupKey{ex, group}}
		id, idErr := strconv.ParseInt(row.Get("id"), 10, 64)
		action, aErr := ParseAction(row.Get("action"))
		switch {
		case idErr != nil || id < 1:
			r.Bad = fmt.Sprintf("id %q is not a file ID", row.Get("id"))
		case aErr != nil:
			r.FileID, r.Bad = id, aErr.Error()
		case row.Beyond() != "":
			r.FileID, r.Bad = id, fmt.Sprintf("%q is outside the columns; put the action under action", row.Beyond())
		default:
			r.FileID, r.Action = id, action
		}
		p.Rows = append(p.Rows, r)
	})
	return p, err
}

// Status is a group's outcome in a plan.
type Status int

const (
	NotDecided Status = iota // every row blank: left alone, so a CSV can be worked through and run several times
	Refused                  // its rows break the group rules or the CSV's structure: nothing is done
	Decided                  // to be checked against the disk and carried out
)

// GroupPlan is what the CSV says about one group.
type GroupPlan struct {
	Key     GroupKey
	Lines   []int // the CSV lines of its rows, in order
	Status  Status
	Reason  string  // why it is refused
	Keep    []int64 // decided: the copies to keep (two or more: a set of records)
	Delete  []int64 // decided: the copies to delete
	Always  bool    // decided: the content is always
	Missing []int64 // the copies exported in the group with no row in the CSV: filo delete checks whether they are gone
}

// Plan applies the group rules (docs/design.md, Duplicates) to each group in the CSV, against exported: for each
// duplicates export of the register, each file's group. A row not as exported (its group edited, a row added, pasted
// from another export or duplicated) refuses every group it touches: the group in its cell and the group it was
// exported in. The other groups are planned as usual. Groups are found by their export and group cells, not by the
// order of the rows, so a sorted CSV is fine. Problems are the rows that cannot be tied to any group.
func Plan(p Parsed, exported map[int64]map[int64]int64) ([]GroupPlan, []Problem) {
	plans := map[GroupKey]*GroupPlan{}
	get := func(k GroupKey) *GroupPlan {
		if plans[k] == nil {
			plans[k] = &GroupPlan{Key: k}
		}
		return plans[k]
	}
	refuse := func(k GroupKey, why string) {
		g := get(k)
		g.Status = Refused
		if !strings.Contains(g.Reason, why) {
			g.Reason = strings.TrimPrefix(g.Reason+"; "+why, "; ")
		}
	}
	type fileKey struct{ export, file int64 }
	type listing struct {
		line  int
		group int64
	}
	seen := map[fileKey]listing{} // where each file was first listed
	rowsOf := map[GroupKey][]Row{}
	for _, r := range p.Rows {
		g := get(r.Key)
		g.Lines = append(g.Lines, r.Line)
		at := fmt.Sprintf("line %d: ", r.Line)
		files, known := exported[r.Key.Export]
		switch {
		case r.Bad != "":
			refuse(r.Key, at+r.Bad)
			continue
		case !known:
			refuse(r.Key, fmt.Sprintf("%sexport %d is not a duplicates export of this register: export again",
				at, r.Key.Export))
			continue
		}
		fk := fileKey{r.Key.Export, r.FileID}
		want, in := files[r.FileID]
		if first, dup := seen[fk]; dup {
			// Every group the file is listed in, and its own, whichever row comes first: sorting must not matter.
			why := fmt.Sprintf("%sfile %d is listed more than once (first on line %d): export again", at,
				r.FileID, first.line)
			refuse(r.Key, why)
			refuse(GroupKey{r.Key.Export, first.group}, why)
			if in {
				refuse(GroupKey{r.Key.Export, want}, why)
			}
			continue
		}
		seen[fk] = listing{r.Line, r.Key.Group}
		switch {
		case !in:
			refuse(r.Key, fmt.Sprintf("%sfile %d is not in export %d (added, or pasted from another export): "+
				"export again", at, r.FileID, r.Key.Export))
			continue
		case want != r.Key.Group:
			why := fmt.Sprintf("%sfile %d is in group %d of export %d, not group %d (its group was edited): "+
				"export again", at, r.FileID, want, r.Key.Export, r.Key.Group)
			refuse(r.Key, why)
			refuse(GroupKey{r.Key.Export, want}, why)
			continue
		}
		rowsOf[r.Key] = append(rowsOf[r.Key], r)
	}
	// A file in groups of two exports (overlapping exports, B3) with different actions: refuse each such group, so
	// what happens never depends on which group is carried out first. Blank is no decision, so never a conflict.
	type decision struct {
		key    GroupKey
		action Action
		line   int
	}
	decided := map[int64][]decision{}
	for k, rows := range rowsOf {
		for _, r := range rows {
			if r.Action != None {
				decided[r.FileID] = append(decided[r.FileID], decision{k, r.Action, r.Line})
			}
		}
	}
	for file, ds := range decided {
		slices.SortFunc(ds, func(a, b decision) int { return cmp.Compare(a.line, b.line) })
		for _, d := range ds[1:] {
			if d.action != ds[0].action {
				why := fmt.Sprintf("file %d is %s on line %d but %s on line %d (exports %d and %d): give it one "+
					"action", file, ds[0].action, ds[0].line, d.action, d.line, ds[0].key.Export, d.key.Export)
				for _, x := range ds {
					refuse(x.key, why)
				}
				break
			}
		}
	}
	// The copies exported in each group the CSV mentions, but with no row in it.
	for k, g := range plans {
		for file, group := range exported[k.Export] {
			if _, listed := seen[fileKey{k.Export, file}]; group == k.Group && !listed {
				g.Missing = append(g.Missing, file)
			}
		}
		slices.Sort(g.Missing)
	}
	for k, g := range plans {
		if g.Status != Refused {
			decide(g, rowsOf[k])
		}
	}
	out := make([]GroupPlan, 0, len(plans))
	for _, g := range plans {
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b GroupPlan) int {
		return cmp.Or(cmp.Compare(a.Key.Export, b.Key.Export), cmp.Compare(a.Key.Group, b.Key.Group))
	})
	return out, p.Problems
}

// decide applies the group rules to a group's rows, all of them as exported.
func decide(g *GroupPlan, rows []Row) {
	var blank, keep, del, always int
	for _, r := range rows {
		switch r.Action {
		case None:
			blank++
		case Keep:
			keep++
			g.Keep = append(g.Keep, r.FileID)
		case Delete:
			del++
			g.Delete = append(g.Delete, r.FileID)
		case Always:
			always++
		}
	}
	slices.Sort(g.Keep)
	slices.Sort(g.Delete)
	refuse := func(why string) {
		g.Status, g.Reason, g.Keep, g.Delete = Refused, why, nil, nil
	}
	switch {
	case blank == len(rows):
		g.Status, g.Keep, g.Delete = NotDecided, nil, nil
	case blank > 0:
		refuse(fmt.Sprintf("every copy needs a value: %d of %d rows are blank", blank, len(rows)))
	case always > 0 && always < len(rows):
		refuse("always is about the content, so it goes on every row")
	case always > 0:
		g.Status, g.Always = Decided, true
	case keep == 0:
		refuse("every copy is delete: no copy would be left; to remove this content entirely, give it a past retention")
	default:
		g.Status = Decided
	}
}
