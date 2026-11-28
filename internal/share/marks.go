package share

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
)

// NoteMarks records the marks a scan of root found under scope (filoDirs: the directories holding .filo, from the
// walk): each that is neither the place of a synced directory this device knows nor a mark it ignores is kept as
// found, to be reported until it is joined or ignored. It writes only when what is found changed.
func NoteMarks(ctx context.Context, s *store.Store, root store.Root, scope string, filoDirs []string,
	now time.Time) error {
	dirs, err := s.SyncedDirs(ctx)
	if err != nil {
		return err
	}
	var found []store.FoundMark
	for _, rel := range filoDirs {
		abs := store.AbsPath(root.Path, rel)
		id, problem := readMarkID(abs)
		skip := false
		for _, sd := range dirs {
			if id != "" && sd.SyncID == id && (sd.State == store.SyncedIgnored || sd.RootID == root.ID && sd.Dir == rel) {
				skip = true // its own place, or a mark the person ignores (wherever it is: a copy does not nag)
			}
		}
		if !skip {
			found = append(found, store.FoundMark{RootID: root.ID, RootPath: root.Path, Dir: rel, SyncID: id,
				Problem: problem})
		}
	}
	_, err = s.SetFoundMarks(ctx, root.ID, scope, found, timeText(now))
	return err
}

// readMarkID reads the ID in the mark in abs/.filo, or why there is none Filo can read.
func readMarkID(abs string) (id, problem string) {
	d, err := journal.Open(abs)
	if err != nil {
		return "", err.Error()
	}
	defer d.Close()
	raw, err := d.Read(journal.MarkName, 0)
	if err != nil {
		return "", "a " + journal.FiloDir + " directory with no mark Filo can read"
	}
	l, err := journal.ReadRecord(raw, journal.KindMark)
	if err != nil {
		return "", "its mark is not one: " + err.Error()
	}
	m, err := l.Mark()
	if err != nil {
		return "", err.Error()
	}
	return m.ID, ""
}

// The kinds of a found mark, as the person is told about it.
const (
	FoundNew     = "new"     // a synced directory this device has not joined: join or ignore it
	FoundMoved   = "moved"   // the mark of a joined synced directory whose place has lost it: confirm the move
	FoundCopy    = "copy"    // the mark of a joined synced directory that is still there: a copy of it
	FoundNested  = "nested"  // inside a joined synced directory, or holding one: cannot be joined
	FoundProblem = "problem" // a .filo with no mark Filo can read
)

// Found is a found mark and what it is.
type Found struct {
	store.FoundMark
	Kind string
	Of   *store.SyncedDir // for FoundMoved, FoundCopy and FoundNested: the synced directory it is of, or inside
}

// FoundMarks returns the marks found, each with what it is.
func FoundMarks(ctx context.Context, s *store.Store) ([]Found, error) {
	marks, err := s.FoundMarks(ctx)
	if err != nil {
		return nil, err
	}
	dirs, err := s.SyncedDirs(ctx)
	if err != nil {
		return nil, err
	}
	var out []Found
	for _, m := range marks {
		f := Found{FoundMark: m, Kind: FoundNew}
		if m.SyncID == "" {
			f.Kind = FoundProblem
		}
		for i, sd := range dirs {
			if sd.State != store.SyncedJoined {
				continue
			}
			switch {
			case m.SyncID != "" && sd.SyncID == m.SyncID:
				f.Kind, f.Of = FoundCopy, &dirs[i]
				there, err := markAt(ctx, s, sd)
				switch {
				case err != nil:
					f.Problem = "whether it moved cannot be told: " + err.Error()
				case !there:
					f.Kind = FoundMoved
				}
			case f.Kind == FoundNew && (sd.Contains(m.RootID, m.Dir) ||
				(sd.RootID == m.RootID && (m.Dir == "" || len(sd.Dir) > len(m.Dir) && sd.Dir[:len(m.Dir)+1] == m.Dir+"/"))):
				f.Kind, f.Of = FoundNested, &dirs[i]
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// ErrNoMarkID is a .filo with no mark Filo can read the ID of: it cannot be ignored, since ignoring is by the ID.
var ErrNoMarkID = errors.New("no mark Filo can read")

// Ignore records that the person chose not to join the mark in directory abs: it is not reported again, here or
// wherever a copy of it turns up. Only the mark's ID is read, so a mark that cannot be joined (its device's journal
// missing, say) can be ignored too.
func Ignore(ctx context.Context, s *store.Store, abs string, now time.Time) error {
	root, rel, err := s.Resolve(ctx, abs)
	if err != nil {
		return err
	}
	id, problem := readMarkID(abs)
	if id == "" {
		return fmt.Errorf("%s: %w (%s): look inside %s, and remove it if it is not a synced directory's", abs,
			ErrNoMarkID, problem, journal.FiloDir)
	}
	dirs, err := s.SyncedDirs(ctx)
	if err != nil {
		return err
	}
	for _, sd := range dirs {
		if sd.SyncID == id {
			return fmt.Errorf("%s holds the mark of %s, which this device knows (%s)", abs, sd.Abs(), sd.State)
		}
	}
	_, err = s.ApplySynced(ctx, store.SyncedChange{Add: &store.SyncedDir{SyncID: id, RootID: root.ID, Dir: rel,
		State: store.SyncedIgnored, AddedUTC: timeText(now)}})
	return err
}
