package cli

import (
	"flag"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/alansav/filo/internal/complete"
)

// TestHelpListsTheRealFlags: the help is the one list of every command's flags (docs/design.md, Terminal output,
// Help and errors; Bash completion, "The help is the list of flags"). Each flag a usage line names is one its command
// really defines (captured when it parses them); and a command's flag list is exactly the flags it defines, across
// its usage lines. A usage line may name fewer than its flag set holds: policies, rules and indexes share one set
// between their subcommands and refuse, with their own message, a flag given to the wrong one. --db, which every
// command takes, is in the overview.
func TestHelpListsTheRealFlags(t *testing.T) {
	ct := newCLITest(t)
	for _, cmd := range slices.Sorted(maps.Keys(commandHelps)) {
		h := commandHelps[cmd]
		listed := map[string]bool{}
		for _, term := range h.flags {
			for _, f := range complete.ParseFlag(term.Name) {
				listed[strings.TrimLeft(f.Name, "-")] = true
			}
		}
		defined := map[string]bool{}
		for _, u := range complete.Usages(h.usage) {
			real := map[string]bool{}
			flagSetMade = func(fs *flag.FlagSet) {
				if fs.Name() == "filo" {
					return // the flags before the command: --db
				}
				fs.VisitAll(func(f *flag.Flag) {
					if f.Name != "db" {
						real[f.Name] = true
					}
				})
			}
			ct.filo(ExitOK, "", false, append(slices.Clone(u.Words), "-h")...)
			flagSetMade = nil
			line := "filo " + strings.Join(u.Words, " ")
			for _, f := range u.Flags {
				if !real[strings.TrimLeft(f.Name, "-")] {
					t.Errorf("%s: its usage line names %s, which it does not define", line, f.Name)
				}
			}
			if u.AllFlags && !maps.Equal(real, listed) {
				t.Errorf("%s: defines %q, its flag list has %q", line, slices.Sorted(maps.Keys(real)),
					slices.Sorted(maps.Keys(listed)))
			}
			maps.Copy(defined, real)
		}
		if got, want := slices.Sorted(maps.Keys(defined)), slices.Sorted(maps.Keys(listed)); !slices.Equal(got, want) {
			t.Errorf("filo %s: defines %q, its flag list has %q", cmd, got, want)
		}
	}
}
