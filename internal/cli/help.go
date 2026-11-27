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
		{Name: "export tags [DIR] -o FILE",
			Text: "write every file under DIR (or every root) with its tags, as CSV"},
		{Name: "update FILE [--dry-run]",
			Text: "record the retentions or tags edited in an exported CSV"},
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
		{Name: "rules [DIR]", Text: "list the directory rules, at DIR and below"},
		{Name: "rules add DIR [PATTERN] POLICY",
			Text: "give POLICY to the files under DIR with no retention, now and as they arrive"},
		{Name: "rules set DIR [PATTERN] POLICY", Text: "change the policy a rule gives new files"},
		{Name: "rules remove DIR [PATTERN]", Text: "remove a rule; the files it set a policy on keep it"},
		{Name: "rules move OLD NEW", Text: "move the rules at OLD and below it to NEW (a rename)"},
		{Name: "rules copy OLD NEW", Text: "copy the rules at OLD and below it to NEW"},
		{Name: "show PATH...", Text: "everything filo holds about a file or directory: retention, history, copies"},
		{Name: "set FILE... [flags]", Text: "set files' file_date, retention or duplicates decision"},
		{Name: "get FILE... --FIELD...", Text: "print fields of files, for scripts: --retention, --keep-until …"},
		{Name: "find [DIR] [flags]", Text: "list the files under DIR (or every root), by name or tag: --name " +
			"'*bill*', --tag type:bill"},
		{Name: "tag FILE... TAG...", Text: "give files tags, such as sender:British Gas or urgent"},
		{Name: "untag FILE... TAG...", Text: "remove tags from files (key: for every value of the key)"},
		{Name: "tags", Text: "list the tags in use, with how many files have each"},
		{Name: "tags rename OLD NEW", Text: "rename a tag, or a key, on every file that has it"},
		{Name: "indexes", Text: "list the tag indexes: HTML pages of a directory's files by tag"},
		{Name: "indexes add [DIR] [--name NAME]", Text: "write a directory's tag index, kept up to date from then on"},
		{Name: "indexes remove [DIR] [--name NAME]", Text: "stop keeping an index, and remove its pages"},
		{Name: "indexes refresh [DIR] [--name NAME]", Text: "write an index's pages again"},
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
			"filo export [DIR] -o FILE [--no-scan] [--workers N]",
			"filo export retention [DIR] -o FILE [--no-scan] [--workers N]",
			"filo export duplicates [DIR] -o FILE [--no-scan] [--workers N]",
			"filo export tags [DIR] -o FILE [--no-scan] [--workers N]",
		},
		before: []string{
			"Scans first (DIR only; every root for export duplicates), then writes every file under DIR (inside a " +
				"root), or under every root, as CSV (UTF-8) to FILE. A root that cannot be scanned stops the export. " +
				"If FILE " +
				"exists, filo asks before replacing it (only a regular file, and only at a terminal). Columns: id, " +
				"export, directory, name, size, modified, file_date, retention, keep_until, tags. " +
				"Fill in retention in a spreadsheet (a period such as 7y counted from file_date, a date, forever, or " +
				"a policy name: filo help policies) and tags (separated by ;, see filo help tag), then run filo " +
				"update FILE. A directory named retention, duplicates or tags is given with ./ before it (./tags). A " +
				"blank file_date is filled in from " +
				"the file name when the name holds one clear date (2023-03-14, 20230314, or 2023-03 for the month's " +
				"last day), marked with ! (!2023-03-14): check it and remove the ! to accept it; a period on a " +
				"row whose date is still marked is refused. On a large system, export one directory at a time.",
			"export retention writes the same without the tags column; export tags (below) only the tags.",
			"export duplicates writes each duplicate group (two or more copies of the same content; hard links to " +
				"one file are one copy) with a copy under DIR, with all its copies. Columns: id, export, group, " +
				"copies, directory, name, size, modified, file_date, retention, keep_until, action. In each group " +
				"you decide on, write keep for the copy to keep and delete for the rest (or k and d); keep on two or " +
				"more copies keeps them as separate files, each with its own retention; always on every row means the " +
				"content belongs everywhere. A group left blank is left alone. filo delete FILE carries the CSV out.",
			"export tags writes every file under DIR with its tags in one cell, separated by ; (see filo help tag). " +
				"Columns: id, export, directory, name, size, modified, tags. Edit the cells, then run filo update " +
				"FILE: each cell replaces its file's tags.",
		},
		flags: []view.Term{
			{Name: "-o FILE", Text: "the CSV to write; an existing file is replaced only after asking"},
			{Name: "--no-scan", Text: "export the register as it is, without scanning first"},
			{Name: "--workers N", Text: "for the scan: files hashed in parallel, 1-64 (default 4; use 1 on a spinning disk)"},
		},
	},
	"update": {
		usage: []string{"filo update FILE [--dry-run] [-y]"},
		before: []string{
			"Reads an exported CSV and applies the file_date, retention and tags you changed. retention is a " +
				"period counted from file_date (7y, 18m, 6w, 30d), forever, or a date (YYYY-MM-DD); file_date is " +
				"a date. keep_until is worked out by filo: leave it alone. Dates in the past are fine: the file is " +
				"then past its retention (see filo delete). Rows can be sorted, filtered or deleted, but not added. " +
				"Each tags cell replaces its file's tags, so an untouched cell changes nothing and an emptied one " +
				"removes them all. A row is refused, with one line saying why, if a value does not parse, a period " +
				"has no file_date, the file changed since the export, its retention or tags were changed after " +
				"the export, or a tag breaks the spelling rules (see filo help tag). The other rows are still " +
				"applied.",
			"If any row clears a retention, removes a tag or names a policy that does not exist yet, filo lists " +
				"them first and asks once (-y answers yes); no, or no terminal without -y, records nothing at all " +
				"(exit 1 when there was no terminal to ask in).",
			"A CSV from export retention is read the same way without tags; a blank retention there clears a " +
				"stored one after asking, and without a terminal those rows are left alone.",
			"A tags CSV (from export tags) is told by its header: each tags cell replaces its file's tags. " +
				"Before removing any tag, filo " +
				"lists those rows and asks (-y answers yes); without a terminal, and without -y, it leaves them " +
				"alone. A row is refused, with one line " +
				"saying why, if for example a tag breaks the spelling rules (see filo help tag), the file's tags " +
				"changed since the export (by tag, untag or tags rename), or the file is listed twice.",
			"A duplicates CSV (from export duplicates) is refused: its decisions are not recorded, but carried " +
				"out by filo delete FILE.",
		},
		flags: []view.Term{
			{Name: "--dry-run", Text: "check everything and write nothing"},
			{Name: "-y", Text: "apply without asking (not for a CSV from export retention)"},
		},
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
				"with one line saying why. A file that cannot be read is never deleted. Directories the run emptied " +
				"are removed, up to the root. Exit code 1 if a file could not be read or deleted.",
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
	"find": {
		usage: []string{"filo find [DIR] [--name PATTERN] [--tag TAG]... [--no-tag TAG]... [-0]",
			"          [--no-scan] [--workers N]"},
		before: []string{
			"Lists the files under DIR, a directory inside a root, or under every root, whose name matches " +
				"PATTERN and whose tags match every --tag and no --no-tag; with neither, every file. Scans first, " +
				"so a file saved since the last scan is found. " +
				"Lists the files present now: not missing files, files deleted by filo or symlinks. A file with " +
				"hard links is listed once.",
			"In a terminal, every file found, with its modified date, size and tags. Piped, one absolute path per line, " +
				"escaped as all output is (a name holding a newline or a backslash is shown escaped, says so on " +
				"stderr and exits 1); use -0 to pass any name to xargs -0 or tar --null -T -.",
		},
		flags: []view.Term{
			{Name: "--name PATTERN", Text: "the file's name, not its directory, ignoring case; * matches any run " +
				"of characters, and is the only wildcard (? and [ match themselves)"},
			{Name: "--tag TAG", Text: "a tag the file has (repeatable: all of them): key:value, key: for any " +
				"value of the key, or a bare value, which finds bare tags only; * in a value matches any run of " +
				"characters, and case is ignored"},
			{Name: "--no-tag TAG", Text: "a tag the file lacks (repeatable): --no-tag sender: lists the files with " +
				"no sender"},
			{Name: "-0", Text: "piped only: the paths as they are, each ended by a NUL, for xargs -0"},
			{Name: "--no-scan", Text: "skip the scan: list from the register as it is"},
			{Name: "--workers N", Text: "for the scan: files hashed in parallel, 1-64 (default 4; use 1 on a spinning disk)"},
		},
	},
	"tag": {
		usage: []string{"filo tag FILE... TAG...", "filo untag FILE... TAG..."},
		before: []string{
			"Gives each file the tags, or removes them. A tag is key:value (sender:British Gas, project:house " +
				"sale) or a bare value (urgent); a file can have any number, and several values for one key. The " +
				"files come first: the arguments that are on the disk are files, and the first that is not starts " +
				"the tags, so quote a tag with spaces. A tag that is also the name of something in the current " +
				"directory is read as a file: run filo from another directory.",
			"Spelling: the first colon ends the key when what is before it is letters, digits, - and _ with a " +
				"letter (10:30 call is a bare tag). Keys are lower case; values keep the case first typed, and match " +
				"in any case: once a file has sender:British Gas, sender:british gas gives another file the same " +
				"tag (filo tags rename changes the spelling). Spaces in a value are trimmed and runs of them made " +
				"one. A tag cannot hold ; or *, and needs a value: sender: alone is a search, or for untag every " +
				"value of the key.",
			"All or nothing: if any file cannot be tagged (a directory, a symlink, outside every root, skipped by " +
				"its root's settings), nothing changes and every refusal is listed. A file filo has not seen yet is " +
				"registered by itself first, taking the tags of any copies of its content; one that looks moved " +
				"from a file the register has is refused until filo scan pairs them.",
		},
		after: []string{
			"Tags follow the file through moves and edits, and a new copy of a tagged file takes its tags. Find " +
				"files by tag with filo find --tag; many files at once with filo export tags and filo update.",
		},
	},
	"tags": {
		usage: []string{"filo tags", "filo tags rename OLD NEW"},
		before: []string{
			"Lists every tag in use with how many files have it (files deleted by filo do not count), and how many " +
				"files have a tag and how many none.",
			"tags rename gives every file that has OLD the tag NEW instead, spelled as typed: it fixes a typo in a " +
				"key or a value, or changes only the case (sender:british gas to sender:British Gas). With key: for " +
				"both (sendr: sender:) it renames the key of every value. A file that has both keeps one.",
		},
	},
	"indexes": {
		usage: []string{"filo indexes", "filo indexes add [DIR] [--name NAME]",
			"filo indexes remove [DIR] [--name NAME] [-y]", "filo indexes refresh [DIR] [--name NAME]"},
		before: []string{
			"A tag index is a set of HTML pages that list the files under a directory, at any depth, by tag, to " +
				"browse and tap instead of typing searches: a main page of keys, a page per key of its values, a " +
				"page per tag of its files, and the files with no tags. Each file opens from its page. The links " +
				"are relative, so the pages work on any device that syncs the directory, and nothing is fetched " +
				"from the network.",
			"add declares an index on DIR (the current directory if not given) and writes its pages: NAME.html in " +
				"DIR, and the other pages and a stylesheet in DIR/.NAME/ (NAME is index unless given). From then " +
				"on every command that changes the register brings them up to date, and says nothing unless it " +
				"cannot. Scans skip both paths.",
			"Filo replaces or removes only the files it wrote, and only while they are as it wrote them: a page " +
				"edited by hand, or a file put in .NAME/, is left alone and reported, and the command exits 1. " +
				"Move it aside, then refresh writes the pages again. filo indexes lists the indexes and what is " +
				"wrong with each.",
			"remove lists what it will remove and asks first. A file that is not Filo's stays, and so does the " +
				"directory holding it; remove then exits 1. An index on a drive that cannot be reached (not " +
				"mounted, lost) cannot have its files removed: remove says so and asks whether to remove the " +
				"index anyway; its files, should they come back, are then registered by the next scan.",
		},
		flags: []view.Term{
			{Name: "--name NAME", Text: "add: the pages' name (letters, digits, - and _); remove and refresh: " +
				"which index of DIR, when it has several"},
			{Name: "-y", Text: "remove: remove without asking"},
		},
	},
	"get": {
		usage: []string{"filo get FILE... --FIELD..."},
		before: []string{
			"Prints fields of each file from the register, for scripts and quick checks, in the order the fields " +
				"are given. One field of one file prints its bare value; several fields of one file print a name " +
				"and value per line; several files print a row each, the values in order and the file last. A " +
				"field with no value prints as empty. Nothing is changed, and nothing is read from the disk.",
			"Piped, values are separated by tabs and the file is its absolute path, last; a tab in a value or a " +
				"name is shown escaped, so the tabs between fields are always filo's.",
		},
		flags: []view.Term{
			{Name: "--file-date", Text: "the date a period counts from (YYYY-MM-DD)"},
			{Name: "--retention", Text: "as typed (7y, forever, a date) or a policy's name"},
			{Name: "--keep-until", Text: "the date it is kept until, or forever"},
			{Name: "--status", Text: "kept, forever, expired, changed, undated, needs_file_date, possible_move, " +
				"unreadable; missing or deleted for a file not present"},
			{Name: "--duplicates", Text: "the content's decision: always, records or counted"},
			{Name: "--size", Text: "in bytes"},
			{Name: "--modified", Text: "its modification time, RFC 3339, local time"},
			{Name: "--sha256", Text: "its content's hash, as sha256sum prints it"},
			{Name: "--root", Text: "the root it is in"},
			{Name: "--first-seen", Text: "the date filo first saw it"},
		},
	},
	"set": {
		usage: []string{"filo set FILE... [flags]"},
		before: []string{
			"Sets the file_date and the retention of each file, as the retention CSV does, for a few files at once: " +
				"a retention is a period (7y, 18m, 6w, 30d, counted from file_date), forever, a date (YYYY-MM-DD) " +
				"or a policy's name. Several flags can be given together. keep_until is worked out again.",
			"All or nothing: if any file cannot be set (a period with no file_date, " +
				"a file missing or not in the register), nothing changes and every refusal is listed. It asks " +
				"only before replacing a value already stored, showing what each was and will be; a file given " +
				"its first values is not asked about. A retention set here replaces a policy a directory rule set.",
			"A policy name that is not a policy yet creates one with no value, and says so: its files need a " +
				"retention until filo policies set NAME VALUE gives it one.",
			"--duplicates is about a content, not one file: always means it belongs everywhere, so none of its " +
				"copies is ever a duplicate; records means the files named, two or more copies of it, are each a " +
				"record with its own retention; counted undoes either (as filo duplicates include FILE does). " +
				"Each lists the copies it covers, and it asks before replacing or undoing a decision already made.",
		},
		flags: []view.Term{
			{Name: "--file-date DATE", Text: "the date a period counts from"},
			{Name: "--no-file-date", Text: "clear the file_date (refused where a period needs it)"},
			{Name: "--retention R", Text: "a period, forever, a date or a policy's name"},
			{Name: "--no-retention", Text: "clear the retention: the files need one again"},
			{Name: "--duplicates D", Text: "always, records or counted: a decision about the files' content"},
			{Name: "--dry-run", Text: "show what would change, and change nothing"},
			{Name: "-y", Text: "replace values without asking"},
		},
		after: []string{
			"A file left past its retention is listed, to check for a typo such as 7d for 7y; nothing is deleted " +
				"until filo delete is run.",
		},
	},
	"show": {
		usage: []string{"filo show PATH... [-r] [--history] [--verify]"},
		before: []string{
			"Shows everything the register holds about each file, one after another: what filo observed (its " +
				"path and root, size, modified time, hash, when it was first seen, where it was before) and what " +
				"was decided (its retention, file_date and keep_until, and when they were set, and its tags), then " +
				"the other files with the same content. Nothing is changed: filo set and filo tag change what was " +
				"decided.",
			"A path is looked up in the register, not on the disk, so a missing file is shown too, and a file " +
				"deleted by filo is found by its last path. The modified line says whether the file on disk is " +
				"still as the last scan saw it, from its size, modified time and inode, without reading it.",
			"A directory shows its root, its files by state, the directory rules tried for its files in the order " +
				"they are tried, then its subdirectories (each with how many files need a decision) and every file " +
				"of its own, with its size and status.",
		},
		flags: []view.Term{
			{Name: "-r, --recursive", Text: "directories: list every directory below it too, each with its files"},
			{Name: "--history", Text: "files: list every path the file has had, newest first"},
			{Name: "--verify", Text: "files: read the file and compare its content with the register's hash"},
		},
	},
	"rules": {
		usage: []string{
			"filo rules [DIR]",
			"filo rules add DIR [PATTERN] POLICY [--dry-run]",
			"filo rules set DIR [PATTERN] POLICY",
			"filo rules remove DIR [PATTERN]",
			"filo rules move OLD NEW [--dry-run]",
			"filo rules copy OLD NEW [--dry-run]",
		},
		before: []string{
			"A rule names a policy for the files under a directory, at any depth, or only those whose name " +
				"matches PATTERN: * is any run of characters, ? one character, in any case. Without a pattern it is " +
				"*. Quote a pattern with * or spaces ('*Statement*'), or the shell replaces it with file names.",
			"A rule sets the policy of a file only if it has no retention at all, and only once: a file with a " +
				"date, forever or a policy (even one with no value) is never touched, nor one whose retention you cleared after " +
				"a rule set it. For a file, the rules of its own directory are tried first, then those of each " +
				"directory above it: the first directory with a matching rule decides. In a directory, the pattern " +
				"with the most characters wins (* is not counted, ? counts as one); two as long are reported, not guessed.",
			"filo rules lists them in the order they are tried, with how many files each set a policy on, and " +
				"flags a directory that is gone and a policy with no value. add adds a rule and sets the policy of " +
				"the files it decides now, and says how many; a policy that does not exist is created with no value. set changes the " +
				"policy a rule gives from now on: the files it set one on keep theirs. remove removes a rule: the " +
				"files it set a policy on keep it. set and remove name the pattern exactly as the rule has it.",
			"move puts the rules at OLD and the directories below it in the same places under NEW (bank/2024 " +
				"goes to banking/2024), as after renaming a directory; copy does the same and keeps them at OLD " +
				"(copy bank/2024 bank/2025 for a new year). Both set the policy of the files under NEW they now " +
				"decide, as add does. Rules are never merged: both are refused if NEW or a directory below it has " +
				"rules already. NEW may be in another root.",
		},
		flags: []view.Term{{Name: "--dry-run",
			Text: "add, move and copy: show the files they would set a policy on, and change nothing"}},
		after: []string{
			"Every scan applies the rules to the files it sees with no retention, and says what they did; filo " +
				"report counts the files with a policy set by rules.",
			"Nothing is asked: a rule only sets a retention. Nothing is deleted except by filo delete, which asks.",
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

// untag shares tag's help: one page for the pair.
func init() { commandHelps["untag"] = commandHelps["tag"] }
