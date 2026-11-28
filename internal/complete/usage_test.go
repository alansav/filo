package complete

import (
	"reflect"
	"testing"
)

func TestParseUsage(t *testing.T) {
	for _, tc := range []struct {
		line string
		want Usage
	}{
		{"filo roots", Usage{Words: []string{"roots"}}},
		{"filo roots add DIR [--include-hidden] [--ignore NAME]... [--no-scan]", Usage{Words: []string{"roots", "add"},
			Args: []Arg{{Name: "DIR"}}, Flags: []Flag{{"--include-hidden", ""}, {"--ignore", "NAME"}, {"--no-scan", ""}}}},
		{"filo roots set DIR [--include-hidden | --skip-hidden] [--ignore NAME]...", Usage{Words: []string{"roots", "set"},
			Args: []Arg{{Name: "DIR"}}, Flags: []Flag{{"--include-hidden", ""}, {"--skip-hidden", ""}, {"--ignore", "NAME"}}}},
		{"filo export [DIR] -o FILE [--no-scan] [--workers N]", Usage{Words: []string{"export"},
			Args: []Arg{{Name: "DIR", Optional: true}}, Flags: []Flag{{"-o", "FILE"}, {"--no-scan", ""}, {"--workers", "N"}}}},
		{"filo export duplicates [DIR] -o FILE", Usage{Words: []string{"export", "duplicates"},
			Args: []Arg{{Name: "DIR", Optional: true}}, Flags: []Flag{{"-o", "FILE"}}}},
		{"filo tag FILE... TAG...", Usage{Words: []string{"tag"},
			Args: []Arg{{Name: "FILE", Repeat: true}, {Name: "TAG", Repeat: true}}}},
		{"filo rules add DIR [PATTERN] POLICY [--dry-run]", Usage{Words: []string{"rules", "add"},
			Args: []Arg{{Name: "DIR"}, {Name: "PATTERN", Optional: true}, {Name: "POLICY"}}, Flags: []Flag{{"--dry-run", ""}}}},
		{"filo set FILE... [flags]", Usage{Words: []string{"set"}, Args: []Arg{{Name: "FILE", Repeat: true}}, AllFlags: true}},
		{"filo get FILE... --FIELD...", Usage{Words: []string{"get"}, Args: []Arg{{Name: "FILE", Repeat: true}},
			AllFlags: true}},
		{"filo show PATH... [-r] [--history]", Usage{Words: []string{"show"}, Args: []Arg{{Name: "PATH", Repeat: true}},
			Flags: []Flag{{"-r", ""}, {"--history", ""}}}},
		{"filo x [DIR]... [--tag TAG]...", Usage{Words: []string{"x"}, Args: []Arg{{Name: "DIR", Optional: true,
			Repeat: true}}, Flags: []Flag{{"--tag", "TAG"}}}},
		{"filo policies add NAME [VALUE]", Usage{Words: []string{"policies", "add"},
			Args: []Arg{{Name: "NAME"}, {Name: "VALUE", Optional: true}}}},
	} {
		if got := ParseUsage(tc.line); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseUsage(%q) =\n%+v\nwant\n%+v", tc.line, got, tc.want)
		}
	}
}

// TestUsagesJoinsContinuations: a usage line too long for one line goes on in the next, which does not start "filo".
func TestUsagesJoinsContinuations(t *testing.T) {
	got := Usages([]string{"filo find [DIR] [--name PATTERN] [-0]", "          [--no-scan] [--workers N]", "filo tags"})
	if len(got) != 2 || len(got[0].Flags) != 4 || got[0].Flags[3] != (Flag{"--workers", "N"}) || got[1].Words[0] != "tags" {
		t.Errorf("Usages = %+v", got)
	}
}

func TestParseFlag(t *testing.T) {
	for term, want := range map[string][]Flag{
		"--file-date DATE": {{"--file-date", "DATE"}},
		"-y":               {{"-y", ""}},
		"-r, --recursive":  {{"-r", ""}, {"--recursive", ""}},
	} {
		if got := ParseFlag(term); !reflect.DeepEqual(got, want) {
			t.Errorf("ParseFlag(%q) = %+v, want %+v", term, got, want)
		}
	}
}
