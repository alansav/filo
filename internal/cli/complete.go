package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alansav/filo/internal/complete"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

// Bash completion (docs/design.md, Bash completion): filo completion bash prints the script, which on Tab runs
// filo __complete LINE WORD and gives bash its answer.

// completionScript is what filo completion bash prints: it hands filo the line up to the cursor and the word bash
// replaces, reads the answer with mapfile (never through word splitting or globbing), and sets bash's options for
// it. No tab and no backslash in it: the Renderer prints it line by line, escaped, unchanged.
const completionScript = `# bash completion for filo: source <(filo completion bash)
_filo() {
    local out
    mapfile -t out < <(command filo __complete "${COMP_LINE:0:COMP_POINT}" "$2" 2>/dev/null)
    [[ ${#out[@]} -gt 0 ]] || return 0
    if [[ ${out[0]} == files ]]; then
        compopt -o filenames
    else
        compopt -o nospace
    fi
    COMPREPLY=("${out[@]:1}")
}
complete -F _filo filo`

// completion is filo completion bash.
func (e *env) completion(args []string) error {
	fl := e.flags("completion")
	pos, err := e.parse(fl, "completion", args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "bash" {
		return usageError{"completion takes one shell: bash"}
	}
	for _, line := range strings.Split(completionScript, "\n") {
		e.view.Line(line)
	}
	return nil
}

// completeCmd is filo __complete LINE WORD: never an error, nothing on stderr; nothing to offer is an empty answer.
func (e *env) completeCmd(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return nil
	}
	c := &completer{e: e, ctx: ctx, l: complete.Parse(args[0], args[1])}
	c.home = e.getenv("HOME")
	c.cwd, _ = os.Getwd()
	mode, cands := "words", []cand(nil)
	if c.l.OK && len(c.l.Args) > 0 {
		mode, cands = c.answer()
	}
	if c.s != nil {
		c.s.Close()
	}
	e.view.Completions(mode, c.format(mode, cands))
	return nil
}

// cand is a candidate: the whole argument it completes to, and whether it is finished (a space follows), not a key
// or a directory to go on typing from.
type cand struct {
	text string
	done bool
}

// The modes of an answer: files, which bash quotes itself (file names: a list shows only their last part, a
// directory gets its /), and words, which Filo quotes and ends itself.
const (
	filesMode = "files"
	wordsMode = "words"
)

type completer struct {
	e         *env
	ctx       context.Context
	l         complete.Line
	home, cwd string
	db        string // --db typed on the line
	s         *store.Store
	opened    bool
	roots     []store.Root
}

// store opens the register read only, once; nil if it cannot be read (completion then works from the disk alone).
func (c *completer) store() *store.Store {
	if c.opened {
		return c.s
	}
	c.opened = true
	db := c.db
	if db == "" {
		db = c.e.getenv("FILO_DB")
	} else {
		db = c.expand(db)
	}
	s, err := store.OpenReadOnly(c.ctx, db)
	if err != nil {
		return nil
	}
	c.s = s
	if c.roots, err = s.Roots(c.ctx); err != nil {
		c.roots = nil
	}
	return s
}

// format keeps the candidates that start with what is typed (in any case for words, as tags match), each as what
// replaces bash's word, and leaves out what cannot be typed.
func (c *completer) format(mode string, cands []cand) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range cands {
		if !complete.Typable(k.text) || seen[k.text] {
			continue
		}
		seen[k.text] = true
		var s string
		var ok bool
		if mode == filesMode {
			if !strings.HasPrefix(k.text, c.l.Current) {
				continue
			}
			s, ok = c.l.Rest(k.text)
		} else {
			if len(k.text) < len(c.l.Current) || !strings.EqualFold(k.text[:len(c.l.Current)], c.l.Current) {
				continue
			}
			s, ok = c.l.Insert(k.text, k.done)
		}
		if ok {
			out = append(out, s)
		}
	}
	return out
}

// answer works out what the argument being completed is, and what to offer for it.
func (c *completer) answer() (string, []cand) {
	words := c.l.Args[1:]
	cur := c.l.Current
	// The flags before the command: --db.
	i := 0
	for i < len(words) && strings.HasPrefix(words[i], "-") {
		if v, ok := flagValue(words[i], "db"); ok {
			c.db = v
		} else if isFlag(words[i], "db") {
			if i+1 == len(words) {
				return filesMode, c.pathsOf(cur, plainPaths)
			}
			c.db = words[i+1]
			i++
		}
		i++
	}
	if i == len(words) {
		switch {
		case strings.HasPrefix(cur, "--db="):
			return filesMode, prefixed("--db=", c.pathsOf(strings.TrimPrefix(cur, "--db="), plainPaths))
		case strings.HasPrefix(cur, "-"):
			return wordsMode, []cand{{"--db", true}}
		}
		return wordsMode, commandCands()
	}
	cmd, rest := words[i], words[i+1:]
	switch cmd {
	case "help", "-h", "--help":
		if len(rest) == 0 {
			return wordsMode, commandCands()
		}
		return wordsMode, nil
	}
	h, ok := commandHelps[cmd]
	if !ok {
		return wordsMode, nil
	}
	return c.command(cmd, h, rest)
}

// commandCands are the commands, for the word after filo (or after filo help).
func commandCands() []cand {
	out := []cand{{"help", true}}
	for name := range commandHelps {
		out = append(out, cand{name, true})
	}
	return out
}

// command completes an argument of cmd, whose arguments before it are rest.
func (c *completer) command(cmd string, h commandHelp, rest []string) (string, []cand) {
	usages := complete.Usages(h.usage)
	list := flagList(h)
	valued := map[string]complete.Flag{"db": {Name: "--db", Value: "PATH"}}
	for _, u := range usages {
		for _, f := range u.Flags {
			if f.Value != "" {
				valued[strings.TrimLeft(f.Name, "-")] = f
			}
		}
	}
	for _, f := range list {
		if f.Value != "" {
			valued[strings.TrimLeft(f.Name, "-")] = f
		}
	}
	// The positional arguments before this one, and a flag waiting for its value (this argument).
	var pos []string
	var pending *complete.Flag
	ended := false
	for j := 0; j < len(rest); j++ {
		w := rest[j]
		switch {
		case !ended && w == "--":
			ended = true
		case !ended && strings.HasPrefix(w, "-") && w != "-":
			name, value, inline := strings.Cut(strings.TrimLeft(w, "-"), "=")
			f, takes := valued[name]
			switch {
			case name == "db" && inline:
				c.db = value
			case takes && !inline && j+1 == len(rest):
				pending = &f
			case takes && !inline:
				if name == "db" {
					c.db = rest[j+1]
				}
				j++
			}
		default:
			pos = append(pos, w)
		}
	}
	// The subcommand, if the first positional argument is one, and the usage lines that apply.
	subs := map[string]bool{}
	for _, u := range usages {
		if len(u.Words) > 1 {
			subs[u.Words[1]] = true
		}
	}
	sub, args := "", pos
	if len(pos) > 0 && subs[pos[0]] {
		sub, args = pos[0], pos[1:]
	}
	var apply []complete.Usage
	for _, u := range usages {
		if (sub == "" && len(u.Words) == 1) || (sub != "" && len(u.Words) > 1 && u.Words[1] == sub) {
			apply = append(apply, u)
		}
	}
	cur := c.l.Current
	switch {
	case pending != nil:
		return c.fetch(flagSource(cmd, sub, *pending), cur, args)
	case !ended && strings.HasPrefix(cur, "-"):
		if name, value, ok := strings.Cut(strings.TrimLeft(cur, "-"), "="); ok {
			if f, takes := valued[name]; takes {
				mode, cands := c.fetch(flagSource(cmd, sub, f), value, args)
				return mode, prefixed(cur[:len(cur)-len(value)], cands)
			}
			return wordsMode, nil
		}
		return wordsMode, flagCands(apply, h)
	case cmd == "tag" || cmd == "untag":
		return c.tagArgs(cmd == "untag", pos)
	}
	mode, cands := "", []cand(nil)
	if len(pos) == 0 {
		for s := range subs {
			cands = append(cands, cand{s, true})
		}
		mode = wordsMode
	}
	for _, u := range apply {
		for _, a := range slots(u.Args, len(args)) {
			k := argSource(cmd, sub, a.Name)
			if k == srcNothing || k == srcUnknown {
				continue
			}
			m, cs := c.fetch(k, cur, args)
			cands = append(cands, cs...)
			if mode == "" || m == filesMode {
				mode = m // files wins: a subcommand reads as a file name there too
			}
		}
	}
	if mode == "" {
		mode = wordsMode
	}
	return mode, cands
}

// flagList is a command's flag list, each name of a flag known by two given apart.
func flagList(h commandHelp) []complete.Flag {
	var out []complete.Flag
	for _, t := range h.flags {
		out = append(out, complete.ParseFlag(t.Name)...)
	}
	return out
}

// flagCands are the flags the usage lines that apply name (their command's whole list for "[flags]"), each by every
// name the flag list gives it (-r and --recursive), and --db.
func flagCands(apply []complete.Usage, h commandHelp) []cand {
	out := []cand{{"--db", true}}
	names := map[string][]complete.Flag{} // a flag's name -> every name of it
	for _, t := range h.flags {
		fs := complete.ParseFlag(t.Name)
		for _, f := range fs {
			names[f.Name] = fs
		}
	}
	for _, u := range apply {
		fs := u.Flags
		if u.AllFlags {
			fs = flagList(h)
		}
		for _, f := range fs {
			if all, ok := names[f.Name]; ok {
				for _, a := range all {
					out = append(out, cand{a.Name, true})
				}
			} else {
				out = append(out, cand{f.Name, true})
			}
		}
	}
	return out
}

// slots are the arguments the argument at index n of a usage line's arguments can be, allowing for optional ones
// left out and repeated ones given again: rules add DIR [PATTERN] POLICY's second is a PATTERN or the POLICY.
func slots(args []complete.Arg, n int) []complete.Arg {
	var out []complete.Arg
	seen := map[int]bool{}
	var walk func(slot, given int)
	walk = func(slot, given int) {
		if slot >= len(args) {
			return
		}
		a := args[slot]
		if given == n {
			if !seen[slot] {
				seen[slot] = true
				out = append(out, a)
			}
		} else {
			walk(slot+1, given+1)
			if a.Repeat {
				walk(slot, given+1)
			}
		}
		if a.Optional {
			walk(slot+1, given)
		}
	}
	walk(0, 0)
	return out
}

// srcKind is where the candidates for an argument or a flag's value come from.
type srcKind int

const (
	srcUnknown         srcKind = iota // a placeholder completion does not know: TestEveryPlaceholderHasASource fails
	srcNothing                        // a new value Filo cannot guess (N, VALUE, a new name, a pattern)
	srcPlainPaths                     // any file name: a file to write (-o FILE, --db PATH)
	srcAnyDirs                        // any directory (roots add)
	srcRootDirs                       // directories inside the roots
	srcCSVPaths                       // .csv files (update, delete FILE)
	srcRegisteredPaths                // files and directories, on disk and in the register
	srcRoots                          // the roots (roots set)
	srcIndexDirs                      // directories with an index (indexes remove, refresh)
	srcIndexNames                     // the names of a directory's indexes (indexes --name)
	srcRuleDirs                       // directories with rules (rules set, remove, move, copy)
	srcRulePatterns                   // the patterns of a directory's rules
	srcNotCounted                     // what filo duplicates lists (duplicates include)
	srcPolicies                       // policy names
	srcTags                           // tags in use
	srcTagsAndKeys                    // tags in use, and key: for each key (tags rename OLD)
	srcDecisions                      // always, records, counted (set --duplicates)
)

// argSource is where an argument of a usage line completes from (docs/design.md, Bash completion, "What each
// argument completes"): an override for the subcommand, or its placeholder word's default.
func argSource(cmd, sub, name string) srcKind {
	switch cmd + " " + sub + " " + name {
	case "roots add DIR":
		return srcAnyDirs
	case "roots set DIR":
		return srcRoots
	case "update  FILE", "delete  FILE":
		return srcCSVPaths
	case "indexes remove DIR", "indexes refresh DIR":
		return srcIndexDirs
	case "rules set DIR", "rules remove DIR", "rules move OLD", "rules copy OLD":
		return srcRuleDirs
	case "rules set PATTERN", "rules remove PATTERN":
		return srcRulePatterns
	case "rules move NEW", "rules copy NEW":
		return srcRootDirs
	case "rules add PATTERN":
		return srcNothing
	case "duplicates include PATH":
		return srcNotCounted
	case "policies add NAME", "policies add VALUE", "policies set VALUE":
		return srcNothing
	case "policies set NAME", "policies move OLD", "policies move NEW", "policies remove NAME", "policies show NAME":
		return srcPolicies
	case "tags rename OLD":
		return srcTagsAndKeys
	case "tags rename NEW":
		return srcTags
	}
	switch name {
	case "DIR":
		return srcRootDirs
	case "FILE", "PATH":
		return srcRegisteredPaths
	case "TAG":
		return srcTags
	case "POLICY":
		return srcPolicies
	}
	return srcUnknown
}

// flagSource is where a flag's value completes from.
func flagSource(cmd, sub string, f complete.Flag) srcKind {
	switch {
	case f.Name == "--db" || f.Value == "FILE" || f.Value == "PATH":
		return srcPlainPaths
	case f.Value == "TAG":
		return srcTags
	case f.Name == "--retention":
		return srcPolicies
	case f.Name == "--duplicates":
		return srcDecisions
	case cmd == "indexes" && f.Name == "--name" && sub != "add":
		return srcIndexNames
	case cmd == "indexes" && f.Name == "--name", f.Name == "--name" && f.Value == "PATTERN", f.Name == "--ignore",
		f.Value == "N", f.Value == "DATE":
		return srcNothing
	}
	return srcUnknown
}

// fetch gets the candidates of a source for typed, the argument or value being completed; args are the positional
// arguments given before it, after the subcommand.
func (c *completer) fetch(k srcKind, typed string, args []string) (string, []cand) {
	switch k {
	case srcPlainPaths:
		return filesMode, c.pathsOf(typed, plainPaths)
	case srcAnyDirs:
		return filesMode, c.pathsOf(typed, anyDirs)
	case srcRootDirs:
		return filesMode, c.pathsOf(typed, rootDirs)
	case srcCSVPaths:
		return filesMode, c.pathsOf(typed, csvPaths)
	case srcRegisteredPaths:
		return filesMode, c.pathsOf(typed, registeredPaths)
	case srcRoots:
		return filesMode, c.registered(typed, c.rootPaths())
	case srcIndexDirs:
		return filesMode, c.registered(typed, c.indexDirs())
	case srcRuleDirs:
		return filesMode, c.registered(typed, c.ruleDirs())
	case srcNotCounted:
		return filesMode, c.registered(typed, c.notCounted())
	case srcIndexNames:
		return wordsMode, c.indexNames(args)
	case srcRulePatterns:
		return wordsMode, c.rulePatterns(args)
	case srcPolicies:
		return wordsMode, c.policyCands()
	case srcTags:
		return wordsMode, c.tagCandsFor(typed, false)
	case srcTagsAndKeys:
		return wordsMode, c.tagCandsFor(typed, true)
	case srcDecisions:
		return wordsMode, []cand{{"always", true}, {"records", true}, {"counted", true}}
	}
	return wordsMode, nil
}

// prefixed puts before each candidate the part of the argument before the value being completed (--db=).
func prefixed(before string, cands []cand) []cand {
	out := make([]cand, len(cands))
	for i, k := range cands {
		out[i] = cand{before + k.text, k.done}
	}
	return out
}

// flagValue reports whether arg is -name=value or --name=value, with the value.
func flagValue(arg, name string) (string, bool) {
	n, v, ok := strings.Cut(strings.TrimLeft(arg, "-"), "=")
	return v, ok && n == name
}

// isFlag reports whether arg is -name or --name.
func isFlag(arg, name string) bool { return arg == "-"+name || arg == "--"+name }

// pathKind is which entries of a directory a path argument completes to.
type pathKind int

const (
	plainPaths      pathKind = iota // every entry: a file to write (-o FILE, --db PATH)
	anyDirs                         // every directory (roots add)
	rootDirs                        // directories inside the roots, and those on the way to one
	csvPaths                        // directories, and .csv files
	registeredPaths                 // files and directories, on disk and in the register (missing and deleted too)
	diskFiles                       // files and directories on disk (tag, untag)
)

// pathsOf completes typed as a path, one level at a time as bash does: the entries of the directory typed so far
// whose names start with the rest. A hidden entry only once the name typed starts with '.'.
func (c *completer) pathsOf(typed string, kind pathKind) []cand {
	if typed == "~" {
		return []cand{{"~/", false}}
	}
	if strings.HasPrefix(typed, "~") && !strings.HasPrefix(typed, "~/") {
		return nil // ~user: not Filo's to expand
	}
	dirPart, prefix := "", typed
	if k := strings.LastIndexByte(typed, '/'); k >= 0 {
		dirPart, prefix = typed[:k+1], typed[k+1:]
	}
	dir := c.abs(dirPart)
	var out []cand
	seen := map[string]bool{}
	add := func(name string, isDir bool) {
		if seen[name] || !strings.HasPrefix(name, prefix) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".")) {
			return
		}
		seen[name] = true
		if isDir {
			out = append(out, cand{dirPart + name + "/", false})
		} else {
			out = append(out, cand{dirPart + name, true})
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.IsDir() {
				isDir = true
			}
		}
		switch kind {
		case anyDirs:
			if !isDir {
				continue
			}
		case rootDirs:
			if !isDir || !c.nearRoot(filepath.Join(dir, name)) {
				continue
			}
		case csvPaths:
			if !isDir && !strings.EqualFold(filepath.Ext(name), ".csv") {
				continue
			}
		}
		add(name, isDir)
	}
	if kind == registeredPaths {
		if root, rel, ok := c.locate(dir); ok {
			if es, err := c.store().EntriesIn(c.ctx, root.ID, rel, prefix, 100); err == nil {
				for _, e := range es {
					add(e.Name, e.Dir)
				}
			}
		}
	}
	return out
}

// abs is the directory a typed directory part names: the current directory for "", the home directory for ~/.
func (c *completer) abs(dirPart string) string {
	switch {
	case dirPart == "":
		return c.cwd
	case strings.HasPrefix(dirPart, "~/"):
		return filepath.Join(c.home, dirPart[2:])
	case filepath.IsAbs(dirPart):
		return filepath.Clean(dirPart)
	}
	return filepath.Join(c.cwd, dirPart)
}

// expand is a typed path, absolute: ~/ the home directory, a relative path from the current directory.
func (c *completer) expand(p string) string {
	if p == "~" {
		return c.home
	}
	return c.abs(p)
}

// real is p with its symlinks resolved, as the register keeps roots; p as it is, cleaned, if it cannot be (a
// directory no longer on disk).
func real(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// locate finds the root holding the directory dir and dir's path in it ('/'-separated, "" for the root).
func (c *completer) locate(dir string) (store.Root, string, bool) {
	if c.store() == nil {
		return store.Root{}, "", false
	}
	p := real(dir)
	var best store.Root
	for _, r := range c.roots {
		if (p == r.Path || within(p, r.Path)) && len(r.Path) > len(best.Path) {
			best = r
		}
	}
	if best.Path == "" {
		return store.Root{}, "", false
	}
	rel, err := filepath.Rel(best.Path, p)
	if err != nil {
		return store.Root{}, "", false
	}
	if rel == "." {
		rel = ""
	}
	return best, filepath.ToSlash(rel), true
}

// within reports whether p is strictly inside dir.
func within(p, dir string) bool {
	return strings.HasPrefix(p, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}

// nearRoot reports whether the directory p is a root, inside one, or on the way to one; with no register, every
// directory is.
func (c *completer) nearRoot(p string) bool {
	if c.store() == nil {
		return true
	}
	p = real(p)
	for _, r := range c.roots {
		if p == r.Path || within(p, r.Path) || within(r.Path, p) {
			return true
		}
	}
	return false
}

// registered completes typed from a list of registered absolute paths, each written as typed starts: from ~/,
// relative to the current directory, or absolute.
func (c *completer) registered(cur string, paths []string) []cand {
	var out []cand
	for _, p := range paths {
		var form string
		switch {
		case strings.HasPrefix(cur, "~"):
			if c.home == "" || (p != c.home && !within(p, c.home)) {
				continue
			}
			form = "~" + strings.TrimPrefix(p, c.home)
			if form == "~" {
				form = "~/"
			}
		case cur == "" || filepath.IsAbs(cur):
			form = p
		default:
			rel, err := filepath.Rel(c.cwd, p)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			form = rel
		}
		out = append(out, cand{form, true})
	}
	return out
}

func (c *completer) rootPaths() []string {
	if c.store() == nil {
		return nil
	}
	var out []string
	for _, r := range c.roots {
		out = append(out, r.Path)
	}
	return out
}

func (c *completer) indexDirs() []string {
	s := c.store()
	if s == nil {
		return nil
	}
	ixs, err := s.TagIndexes(c.ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, ix := range ixs {
		out = append(out, ix.DirAbs())
	}
	return out
}

// indexNames are the names of the indexes on the directory given (args[0]), or the current directory.
func (c *completer) indexNames(args []string) []cand {
	s := c.store()
	if s == nil {
		return nil
	}
	dir := c.cwd
	if len(args) > 0 {
		dir = c.expand(args[0])
	}
	dir = real(dir)
	ixs, err := s.TagIndexes(c.ctx)
	if err != nil {
		return nil
	}
	var out []cand
	for _, ix := range ixs {
		if ix.DirAbs() == dir {
			out = append(out, cand{ix.Name, true})
		}
	}
	return out
}

func (c *completer) ruleDirs() []string {
	s := c.store()
	if s == nil {
		return nil
	}
	rs, err := s.Rules(c.ctx, 0, "")
	if err != nil {
		return nil
	}
	var out []string
	for _, r := range rs {
		out = append(out, filepath.Join(r.RootPath, filepath.FromSlash(r.Dir)))
	}
	return out
}

// rulePatterns are the patterns of the rules at the directory given (args[0]).
func (c *completer) rulePatterns(args []string) []cand {
	if len(args) == 0 || c.store() == nil {
		return nil
	}
	root, rel, ok := c.locate(c.expand(args[0]))
	if !ok {
		return nil
	}
	rs, err := c.s.Rules(c.ctx, root.ID, rel)
	if err != nil {
		return nil
	}
	var out []cand
	for _, r := range rs {
		if r.Dir == rel {
			out = append(out, cand{r.Pattern, true})
		}
	}
	return out
}

// notCounted are the paths filo duplicates lists: the excluded directories, and the copies of the contents kept out
// of the groups on purpose.
func (c *completer) notCounted() []string {
	s := c.store()
	if s == nil {
		return nil
	}
	var out []string
	if dirs, err := s.ExcludedDirs(c.ctx); err == nil {
		for _, d := range dirs {
			out = append(out, filepath.Join(d.RootPath, filepath.FromSlash(d.Path)))
		}
	}
	if always, records, err := s.AlwaysAndRecords(c.ctx); err == nil {
		for _, a := range always {
			for _, cp := range a.Copies {
				out = append(out, filepath.Join(cp.RootPath, filepath.FromSlash(cp.Path)))
			}
		}
		for _, r := range records {
			for _, cp := range r.Copies {
				out = append(out, filepath.Join(cp.RootPath, filepath.FromSlash(cp.Path)))
			}
		}
	}
	return out
}

func (c *completer) policyCands() []cand {
	s := c.store()
	if s == nil {
		return nil
	}
	ps, err := s.Policies(c.ctx)
	if err != nil {
		return nil
	}
	var out []cand
	for _, p := range ps {
		out = append(out, cand{p.Name, true})
	}
	return out
}

// tagCandsFor completes typed as a tag, from the tags in use; with keys, key: for each key too, a finished argument
// (tags rename OLD).
func (c *completer) tagCandsFor(typed string, keys bool) []cand {
	s := c.store()
	if s == nil {
		return nil
	}
	sum, err := s.TagSummary(c.ctx)
	if err != nil {
		return nil
	}
	var in []tags.Tag
	for _, t := range sum.Tags {
		in = append(in, t.Tag)
	}
	return tagCands(in, typed, keys)
}

// tagCands completes typed from the tags ts, in two steps (docs/design.md, Bash completion, "Tags"): before a colon,
// each key as key: (to go on typing) and the bare tags; after a key and its colon, that key's values. With keys,
// key: is a finished argument (it stands for every value of the key).
func tagCands(ts []tags.Tag, typed string, keys bool) []cand {
	var out []cand
	key, _, hasKey := strings.Cut(typed, ":")
	if hasKey && tags.ValidKey(strings.ToLower(key)) {
		k := strings.ToLower(key)
		if keys {
			out = append(out, cand{k + ":", true})
		}
		for _, t := range ts {
			if t.Key == k {
				out = append(out, cand{t.String(), true})
			}
		}
		return out
	}
	seen := map[string]bool{}
	for _, t := range ts {
		if t.Key == "" {
			out = append(out, cand{t.Value, true})
		} else if !seen[t.Key] {
			seen[t.Key] = true
			out = append(out, cand{t.Key + ":", keys})
		}
	}
	return out
}

// tagArgs completes an argument of filo tag or untag, by the command's own rule (splitFilesTags): the arguments on
// disk from the first are the files, the first that is not starts the tags. While every argument before is a file,
// this one can be a file on disk or a tag; after a tag, only a tag. untag offers only the tags the files named have.
func (c *completer) tagArgs(untag bool, pos []string) (string, []cand) {
	files := 0
	for files < len(pos) {
		if _, err := os.Lstat(c.expand(pos[files])); err != nil {
			break
		}
		files++
	}
	var cands []cand
	if files == len(pos) {
		cands = c.pathsOf(c.l.Current, diskFiles)
		if files == 0 {
			return wordsMode, cands // the files come first
		}
	}
	if !untag {
		return wordsMode, append(cands, c.tagCandsFor(c.l.Current, false)...)
	}
	return wordsMode, append(cands, tagCands(c.tagsOf(pos[:files]), c.l.Current, true)...)
}

// tagsOf are the tags the files at paths have, each once.
func (c *completer) tagsOf(paths []string) []tags.Tag {
	if c.store() == nil {
		return nil
	}
	var ids []int64
	for _, p := range paths {
		abs := real(c.expand(p))
		root, rel, ok := c.locate(filepath.Dir(abs))
		if !ok {
			continue
		}
		f, err := c.s.FileAt(c.ctx, root.ID, pathJoin(rel, filepath.Base(abs)))
		if err == nil {
			ids = append(ids, f.FileID)
		}
	}
	byFile, err := c.s.FileTags(c.ctx, ids)
	if err != nil {
		return nil
	}
	var out []tags.Tag
	for _, id := range ids {
		for _, t := range byFile[id] {
			if !slices.ContainsFunc(out, t.Same) {
				out = append(out, t)
			}
		}
	}
	return out
}

// pathJoin joins a '/'-separated directory in a root ("" for the root) and a name.
func pathJoin(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}
