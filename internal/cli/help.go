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
			"Writes every file under DIR (inside a root), or under every root, as CSV (UTF-8) to FILE, which must " +
				"not exist. Columns: id, export, directory, name, size, modified, file_date, retention, keep_until. " +
				"Fill in retention in a spreadsheet, then run filo update FILE. A blank file_date is filled in from " +
				"the file name when the name holds one clear date (2023-03-14, 20230314, or 2023-03 for the month's " +
				"last day); check it. On a large system, export one directory at a time.",
			"export duplicates writes each duplicate group (two or more copies of the same content; hard links to " +
				"one file are one copy) with a copy under DIR, with all its copies. Columns: id, export, group, " +
				"copies, directory, name, size, modified, file_date, retention, keep_until, delete. Carrying out the " +
				"decisions in it is being rebuilt: filo update no longer records them, and filo delete FILE will " +
				"carry the CSV out.",
		},
		flags: []view.Term{
			{Name: "-o FILE", Text: "the CSV to write; it must not exist"},
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
		usage: []string{"filo delete [DIR] [-y]"},
		before: []string{
			"Lists the files past their retention under DIR (inside a root), or under every root, whose content " +
				"has not changed since the retention was set, then asks: all, select one by one, list them all, or " +
				"none [a/s/l/N]. In a terminal the list shows 10 entries; l lists every one. Select goes file by " +
				"file: d delete, r new retention, s skip or q quit. Enter, or an answer filo does not know, never deletes anything. Without a terminal it only " +
				"lists. Deletion is permanent: there is no recycle bin, so keep backups.",
		},
		flags: []view.Term{{Name: "-y", Text: "delete them all without asking"}},
		after: []string{
			"Just before deleting, each file is read again. It is deleted only if it is still at its path as the " +
				"same file and its content is still the content its retention was set on; otherwise it is skipped " +
				"with one line saying why. A file that cannot be read is never deleted. Folders the run emptied are " +
				"removed, up to the root. Exit code 1 if a file could not be read or deleted.",
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
