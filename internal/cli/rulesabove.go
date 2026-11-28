package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alansav/filo/internal/report"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// Rules on a directory above a synced directory (docs/design.md, Sharing decisions between devices, What is shared):
// such a rule is not shared (what it dates inside is, file by file), so the person is shown each one and asked to
// copy it to the synced directory's top, move it there, or leave it: when the directory is marked or joined, and
// when such a rule is added. No terminal, or -y, leaves it.

// rulesAbove returns the rules on directories above synced directory sd, in its root.
func rulesAbove(ctx context.Context, s *store.Store, sd store.SyncedDir) ([]store.Rule, error) {
	if sd.Dir == "" {
		return nil, nil
	}
	rs, err := s.Rules(ctx, sd.RootID, "")
	if err != nil {
		return nil, err
	}
	var out []store.Rule
	for _, r := range rs {
		if r.RootID == sd.RootID && (r.Dir == "" || strings.HasPrefix(sd.Dir, r.Dir+"/")) {
			out = append(out, r)
		}
	}
	return out, nil
}

// syncedUnder returns the joined synced directories at or under dir of root rootID: those a rule there covers.
func syncedUnder(ctx context.Context, s *store.Store, rootID int64, dir string) ([]store.SyncedDir, error) {
	dirs, err := s.SyncedDirs(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.SyncedDir
	for _, sd := range dirs {
		if sd.State == store.SyncedJoined && sd.RootID == rootID && sd.Dir != dir &&
			(dir == "" || strings.HasPrefix(sd.Dir, dir+"/")) {
			out = append(out, sd)
		}
	}
	return out, nil
}

// askRulesAbove shows each rule in rs (on a directory above synced directory sd) and asks what to do with it:
// copy it to sd's top, move it there, or leave it. With no terminal, or yes (-y), each is left, and listed.
func (e *env) askRulesAbove(ctx context.Context, s *store.Store, sd store.SyncedDir, rs []store.Rule, yes bool) error {
	if len(rs) == 0 {
		return nil
	}
	r := e.view
	r.Blank()
	r.Section("Rules above "+r.Tilde(sd.Abs()), report.Count(int64(len(rs))))
	r.Paragraph(0, "These rules cover the synced directory but are not shared: the other devices get what they "+
		"set file by file, not the rules. Copied or moved to its top, a rule is shared; there it is nearer than "+
		"the rules on the directories between, so it may set other policies than today.")
	ask := !yes && e.canPrompt()
	for _, rule := range rs {
		where := e.ruleWhere(rule.RootPath, rule.Dir)
		_, _, run, err := s.PreviewAddRule(ctx, sd.RootID, sd.Dir, rule.Pattern, rule.Policy, today(), now())
		preview := "would set a policy on " + report.Plural(int64(len(run.Dated)), "file", "files") + " there now"
		if err != nil {
			preview = "cannot be copied there: " + err.Error()
		}
		r.Count(view.Neutral, fmt.Sprintf("%s on %s: %s", ruleTo(rule.Pattern, rule.Policy, rule.PolicyValue),
			where, preview))
		if !ask || err != nil {
			continue
		}
		if err := e.outputErr(); err != nil {
			return err
		}
		pr := newPrompter(e.stdin)
		pr.end, pr.check = r.Blank, e.outputErr
		choice, err := e.askChoice(ctx, pr, "Copy it to the synced directory, move it there, or leave it?",
			[]view.Choice{{Key: "c", Word: "copy"}, {Key: "m", Word: "move"}, {Key: "l", Word: "leave"}}, "l")
		pr.close()
		if err != nil {
			return err
		}
		switch choice {
		case "c", "m":
			if _, _, _, err := s.AddRule(ctx, sd.RootID, sd.Dir, rule.Pattern, rule.Policy, today(), now()); err != nil {
				r.Count(view.Refused, "not copied: "+err.Error())
				continue
			}
			if choice == "m" {
				if _, err := s.RemoveRule(ctx, rule.RootID, rule.Dir, rule.Pattern); err != nil &&
					!errors.Is(err, store.ErrNoRule) {
					return err
				}
				r.Count(view.Done, "moved to "+r.Tilde(sd.Abs()))
			} else {
				r.Count(view.Done, "copied to "+r.Tilde(sd.Abs()))
			}
		default:
			r.Count(view.Neutral, "left as it is")
		}
	}
	if !ask {
		r.Next("Left as they are: copy one with filo rules add " + shellQuote(sd.Abs()) + " PATTERN POLICY")
	}
	return nil
}
