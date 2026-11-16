package cli

import "github.com/alansav/filo/internal/view"

// Help (docs/design.md, Terminal output, Help and errors). The text is stored unwrapped: the Renderer wraps it to
// the terminal's width, or 80 columns when piped.

// helpCol is the column the descriptions of commands and flags start at.
const helpCol = 27

// overview is filo help: what filo is, and its commands.
var overview = struct {
	about    string
	commands []view.Term
	after    string
}{
	about: "keeps a register of your files, where they live, and how long to keep them",
	commands: []view.Term{
		{Name: "roots", Text: "list roots and their settings"},
		{Name: "roots add DIR [flags]", Text: "register a directory as a root, then scan it"},
		{Name: "roots set DIR [flags]", Text: "replace a root's settings"},
		{Name: "scan [DIR]", Text: "scan every root, or DIR inside a root"},
		{Name: "report [--top N]", Text: "totals, files needing a retention, largest files, duplicates, events"},
		{Name: "export retention [DIR] -o FILE",
			Text: "write every file under DIR (or every root) with its retention, as CSV"},
		{Name: "export duplicates [DIR] -o FILE",
			Text: "write the duplicate groups with a copy under DIR, as CSV"},
		{Name: "update FILE [--dry-run]",
			Text: "record the retentions edited in an exported CSV"},
		{Name: "delete [DIR] [-y]",
			Text: "delete the files past their retention, after asking"},
		{Name: "delete FILE [-y]",
			Text: "carry out a duplicates CSV: delete the copies marked delete, after asking"},
		{Name: "duplicates", Text: "list what is not counted as duplicates"},
		{Name: "duplicates exclude DIR", Text: "never count the files under DIR as duplicates"},
		{Name: "duplicates include PATH", Text: "undo an exclusion, a content marked always or files kept as separate files"},
		{Name: "policies", Text: "list the retention policies, and what needs attention"},
		{Name: "policies add NAME [VALUE]", Text: "create a policy: a named retention such as bills = 7y"},
		{Name: "policies set NAME VALUE", Text: "change a policy, and so every file using it"},
		{Name: "policies move OLD NEW", Text: "move a policy's files to another, or rename it"},
		{Name: "policies remove NAME", Text: "delete a policy no file uses"},
		{Name: "policies show NAME", Text: "a policy's value and its files"},
		{Name: "help [COMMAND]", Text: "this overview, or a command's flags and rules"},
	},
	after: "Every command accepts --db PATH (default: $FILO_DB, then the user data directory).",
}

// commandHelp is one command's help: its usage lines, what it does, its flags, and the rules after them.
type commandHelp struct {
	usage  []string
	before []string // paragraphs before the flags
	flags  []view.Term
	after  []string // paragraphs after the flags
}

var commandHelps = map[string]commandHelp{
	"roots": {
		usage: []string{
			"filo roots",
			"filo roots add DIR [--include-hidden] [--ignore NAME]... [--no-scan]",
			"filo roots set DIR [--include-hidden | --skip-hidden] [--ignore NAME]...",
		},
		before: []string{"A root's settings apply to every scan of it, whole or partial."},
		flags: []view.Term{
			{Name: "--include-hidden", Text: "also scan hidden files and directories (names starting with .)"},
			{Name: "--skip-hidden", Text: "skip them again (roots set only; the default)"},
			{Name: "--ignore NAME", Text: "skip entries with this name, at any depth (repeatable); node_modules, " +
				"__pycache__ and .filo are always skipped"},
			{Name: "--no-scan", Text: "roots add only: register the root without scanning it"},
		},
		after: []string{
			"roots add scans the new root straight away; --no-scan only registers it. roots set replaces the whole " +
				"set, so name every --ignore you want to keep.",
			"A root that contains existing roots on the same filesystem takes them in, keeping their files' history " +
				"and retentions; its settings then apply to all of it. A directory inside an existing root is " +
				"already covered: scan it with filo scan DIR.",
		},
	},
	"scan": {
		usage: []string{"filo scan [DIR] [--workers N] [--allow-empty]"},
		before: []string{"Scans DIR, which must be inside a root (a partial scan), or every root if DIR is omitted, " +
			"with each root's settings (see filo help roots)."},
		flags: []view.Term{
			{Name: "--workers N", Text: "files hashed in parallel, 1-64 (default 4; use 1 on a spinning disk)"},
			{Name: "--allow-empty", Text: "record an empty scan of a root on a mounted filesystem"},
		},
	},
	"report": {
		usage: []string{"filo report [--top N]"},
		before: []string{
			"Shows each root's totals and last scan, and the files needing a retention with the directories " +
				"holding most of them.",
			"Then the largest files, duplicate groups with reclaimable space, broken symlinks, unreadable files and " +
				"recent events (moves and missing files among them).",
		},
		flags: []view.Term{{Name: "--top N", Text: "how many entries each list shows (default 10)"}},
	},
	"export": {
		usage: []string{
			"filo export retention [DIR] -o FILE",
			"filo export duplicates [DIR] -o FILE",
		},
		before: []string{
			"Writes every file under DIR (inside a root), or under every root, as CSV (UTF-8) to FILE. If FILE " +
				"exists, filo asks before replacing it (only a regular file, and only at a terminal). Columns: id, export, directory, name, size, modified, file_date, retention, keep_until. " +
				"Fill in retention in a spreadsheet (a period such as 7y counted from file_date, a date, forever, or " +
				"a policy name: filo help policies), then run filo update FILE. A blank file_date is filled in from " +
				"the file name when the name holds one clear date (2023-03-14, 20230314, or 2023-03 for the month's " +
				"last day); check it. On a large system, export one directory at a time.",
			"export duplicates writes each duplicate group (two or more copies of the same content; hard links to " +
				"one file are one copy) with a copy under DIR, with all its copies. Columns: id, export, group, " +
				"copies, directory, name, size, modified, file_date, retention, keep_until, action. In each group " +
				"you decide on, write keep for the copy to keep and delete for the rest (or k and d); keep on two or " +
				"more copies keeps them as separate files, each with its own retention; always on every row means the " +
				"content belongs everywhere. A group left blank is left alone. filo delete FILE carries the CSV out.",
		},
		flags: []view.Term{
			{Name: "-o FILE", Text: "the CSV to write; an existing file is replaced only after asking"},
		},
	},
	"update": {
		usage: []string{"filo update FILE [--dry-run]"},
		before: []string{
			"Reads an exported retention CSV and applies the file_date and retention you changed. retention is a " +
				"period counted from file_date (7y, 18m, 6w, 30d), forever, or a date (YYYY-MM-DD); file_date is " +
				"a date. keep_until is worked out by filo: leave it alone. Dates in the past are fine: the file is " +
				"then past its retention (see filo delete). Rows can be sorted, filtered or deleted, but not added. " +
				"A blank retention clears a stored one; filo lists those files and asks first, and without a " +
				"terminal leaves them alone. A row is refused, with one line saying why, if a value does not parse, " +
				"a period has no file_date, the file changed since the export, or its retention was changed from a " +
				"later export. The other rows are still applied.",
			"A duplicates CSV (from export duplicates) is refused: its decisions are not recorded, but carried " +
				"out by filo delete FILE.",
		},
		flags: []view.Term{{Name: "--dry-run", Text: "check everything and write nothing"}},
	},
	"delete": {
		usage: []string{"filo delete [DIR] [-y]", "filo delete FILE [-y]"},
		before: []string{
			"Lists the files past their retention under DIR (inside a root), or under every root, whose content " +
				"has not changed since the retention was set, then asks: all, select one by one, list them all, or " +
				"none [a/s/l/N]. In a terminal the list shows 10 entries; l lists every one. Select goes file by " +
				"file: d delete, r new retention, s skip or q quit. Enter, or an answer filo does not know, never deletes anything. Without a terminal it only " +
				"lists. Deletion is permanent: there is no recycle bin, so keep backups.",
		},
		flags: []view.Term{{Name: "-y", Text: "delete them all (or do all of the CSV) without asking"}},
		after: []string{
			"Just before deleting, each file is read again. It is deleted only if it is still at its path as the " +
				"same file and its content is still the content its retention was set on; otherwise it is skipped " +
				"with one line saying why. A file that cannot be read is never deleted. Folders the run emptied are " +
				"removed, up to the root. Exit code 1 if a file could not be read or deleted.",
			"A file kept until a date is past its retention on that date: kept until 2026-12-31, it is listed from " +
				"2026-12-31 on. Nothing is deleted then; it is listed the next time filo delete runs.",
			"filo delete FILE carries out a duplicates CSV (filo export duplicates). Each group is checked against " +
				"the disk: a group any of whose files changed or moved since the export is skipped, and a copy gone " +
				"since is ignored. It lists what it will delete, what it will record (contents marked always, " +
				"files kept as separate files) and what it won't touch, then asks: yes, select group by group, list, or no [y/s/l/N]. A " +
				"copy is deleted only while a copy kept is still there with the same content, and only if a copy " +
				"kept lasts at least as long (its keep_until). A CSV can be edited further and run again: groups " +
				"done are reported, and only what is still as exported is acted on. Exit code 1 if the CSV refuses " +
				"a group.",
		},
	},
	"duplicates": {
		usage: []string{
			"filo duplicates",
			"filo duplicates exclude DIR",
			"filo duplicates include PATH",
		},
		before: []string{
			"Lists what is not counted as duplicates: excluded directories, contents marked always and identical " +
				"files kept as separate files. Both of the last two are made in a duplicates CSV (always on every row of a group, " +
				"or keep on two or more copies) and carried out by filo delete FILE.",
			"exclude makes the files under DIR, a directory inside a root, never count as duplicates, of each " +
				"other or of files elsewhere. They are still scanned and their retentions are unchanged. It " +
				"replaces the exclusions under DIR.",
			"include undoes one of them: for a directory, its exclusion; for a file, its content marked always, or " +
				"the whole set of identical files kept as separate files. PATH may be gone from the disk.",
		},
	},
	"policies": {
		usage: []string{
			"filo policies",
			"filo policies add NAME [VALUE]",
			"filo policies set NAME VALUE [-y]",
			"filo policies move OLD NEW [-y]",
			"filo policies remove NAME",
			"filo policies show NAME",
		},
		before: []string{
			"A policy is a named retention, written once and typed in the retention column of a retention CSV in " +
				"place of a period: bills = 7y. Changing it changes every file using it. Its value is a period " +
				"(counted from each file's own file_date), a date, or forever; a name typed in a CSV that is not a " +
				"policy yet creates one with no value, after filo update asks.",
			"filo policies lists them, with what needs attention: no value, a date passed, files without a " +
				"file_date. add creates one; set gives it a value or changes it (it asks first if files would be " +
				"put past their retention, or left without a file_date to count from); move moves every file to " +
				"another policy (into a new name it is a rename); remove deletes a policy no file uses; show lists " +
				"its files.",
		},
		flags: []view.Term{{Name: "-y", Text: "set and move: go ahead without asking"}},
		after: []string{
			"A file kept until 2026-12-31 is past its retention on 2026-12-31: it is listed by the next filo " +
				"delete, which asks before deleting anything.",
		},
	},
}

// showOverview writes filo help.
func showOverview(v *view.Renderer) {
	v.Heading("filo", view.Where{}, overview.about)
	v.Blank()
	v.Section("Commands")
	v.Terms(2, helpCol, overview.commands)
	v.Blank()
	v.Paragraph(0, overview.after)
	v.Next("Next: filo help COMMAND for a command's flags and rules")
}

// showCommandHelp writes filo help COMMAND (and COMMAND -h).
func showCommandHelp(v *view.Renderer, h commandHelp) {
	for _, u := range h.usage {
		v.Section(u)
	}
	for _, p := range h.before {
		v.Blank()
		v.Paragraph(2, p)
	}
	if len(h.flags) > 0 {
		v.Blank()
		v.Section("Flags")
		v.Terms(2, helpCol, h.flags)
	}
	for _, p := range h.after {
		v.Blank()
		v.Paragraph(2, p)
	}
}
