package complete

import (
	"strings"
)

// Usage is one usage line of the help, read for completion (docs/design.md, Bash completion, "The help is the list
// of flags"): "filo roots add DIR [--include-hidden] [--ignore NAME]... [--no-scan]".
type Usage struct {
	Words    []string // the command and subcommand: roots add
	Args     []Arg    // the positional arguments, in order
	Flags    []Flag   // the flags it names
	AllFlags bool     // it takes its command's whole flag list: "[flags]" or "--FIELD..."
}

// Arg is a positional argument: its placeholder word (DIR, FILE, TAG) and whether it may be left out or repeated.
type Arg struct {
	Name     string
	Optional bool
	Repeat   bool
}

// Flag is a flag (--name or -y) and its value's placeholder word ("" for a flag with no value).
type Flag struct {
	Name, Value string
}

// Usages reads a command's usage lines: each starts "filo "; a line that does not continues the one before it.
func Usages(lines []string) []Usage {
	var joined []string
	for _, l := range lines {
		if !strings.HasPrefix(l, "filo ") && len(joined) > 0 {
			joined[len(joined)-1] += " " + strings.TrimSpace(l)
			continue
		}
		joined = append(joined, l)
	}
	var out []Usage
	for _, l := range joined {
		out = append(out, ParseUsage(l))
	}
	return out
}

// ParseUsage reads one usage line. Literal lower-case words after "filo" are the command and subcommand; then
// "[...]" is optional, "..." after an argument or a bracket repeats it, "-o FILE" is a flag with a value, "[--a | --b]"
// is two flags, and "[flags]" or "--FIELD..." stands for the command's whole flag list.
func ParseUsage(line string) Usage {
	var u Usage
	toks := strings.Fields(strings.TrimPrefix(line, "filo"))
	words := true
	for i := 0; i < len(toks); i++ {
		orig := toks[i]
		t := orig
		repeat := strings.HasSuffix(t, "...")
		t = strings.TrimSuffix(t, "...")
		if words && isLiteral(t) {
			u.Words = append(u.Words, t)
			continue
		}
		words = false
		switch {
		case t == "[flags]" || t == "--FIELD":
			u.AllFlags = true
		case strings.HasPrefix(t, "["):
			// A bracket may hold several tokens: [--ignore NAME], [--include-hidden | --skip-hidden].
			group := []string{strings.TrimPrefix(orig, "[")}
			for !closes(group[len(group)-1]) && i+1 < len(toks) {
				i++
				group = append(group, toks[i])
			}
			last := group[len(group)-1]
			repeat = strings.HasSuffix(last, "]...")
			group[len(group)-1] = strings.TrimSuffix(strings.TrimSuffix(last, "..."), "]")
			u.addGroup(group, true, repeat)
		case strings.HasPrefix(t, "-"):
			f := Flag{Name: t}
			if i+1 < len(toks) && isPlaceholder(toks[i+1]) {
				i++
				f.Value = toks[i]
			}
			u.Flags = append(u.Flags, f)
		default:
			u.Args = append(u.Args, Arg{Name: t, Repeat: repeat})
		}
	}
	return u
}

// addGroup adds what one bracket holds: flags (with their values, | between alternatives) or one argument.
func (u *Usage) addGroup(group []string, optional, repeat bool) {
	for j := 0; j < len(group); j++ {
		g := group[j]
		switch {
		case g == "|":
		case strings.HasPrefix(g, "-"):
			f := Flag{Name: g}
			if j+1 < len(group) && isPlaceholder(group[j+1]) {
				j++
				f.Value = group[j]
			}
			u.Flags = append(u.Flags, f)
		default:
			u.Args = append(u.Args, Arg{Name: g, Optional: optional, Repeat: repeat})
		}
	}
}

// ParseFlag reads an entry of a command's flag list: "--file-date DATE", "-y".
func ParseFlag(term string) Flag {
	name, value, _ := strings.Cut(term, " ")
	return Flag{Name: name, Value: value}
}

// isLiteral reports a command or subcommand word: lower-case letters only.
func isLiteral(t string) bool {
	if t == "" {
		return false
	}
	for _, r := range t {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// isPlaceholder reports a placeholder word, as a flag's value: upper-case letters (FILE, N, NAME).
func isPlaceholder(t string) bool {
	t = strings.TrimSuffix(t, "...")
	if t == "" {
		return false
	}
	for _, r := range t {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// closes reports a token that ends a bracket: "NAME]" or "NAME]...".
func closes(t string) bool { return strings.HasSuffix(strings.TrimSuffix(t, "..."), "]") }
