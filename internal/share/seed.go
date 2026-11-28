package share

import (
	"context"
	"time"

	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/store"
)

// Seed writes to this device's journal in synced directory sd the decisions it holds about what is there that are
// not in effect there yet (docs/design.md, Sharing decisions between devices, Identity cases, seeding): every file's
// date, retention and tags, and the settings. On marking, that is everything decided so far; on joining, after the
// other devices' decisions were taken in, only what differs, which becomes a conflict. It returns the problems met.
func Seed(ctx context.Context, s *store.Store, sd store.SyncedDir, now time.Time) ([]string, error) {
	me, err := s.Device(ctx)
	if err != nil {
		return nil, err
	}
	pb, problem, err := startPub(ctx, s, me, sd)
	if err != nil || pb == nil {
		if problem != "" {
			return []string{sd.Abs() + ": " + problem}, err
		}
		return nil, err
	}
	pb.seeding = true // what was decided before: it replaces nothing, so a difference is a conflict
	files, err := s.Files(ctx, retention.Today(now), store.Filter{RootID: sd.RootID, Scope: sd.Dir})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(files))
	for i, f := range files {
		ids[i] = f.FileID
	}
	tagsOf, err := s.FileTags(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if _, ok := relIn(sd, sd.RootID, f.Path); !ok || f.Kind != "file" {
			continue
		}
		p := store.FilePlace{FileID: f.FileID, RootID: sd.RootID, Path: f.Path, Hash: f.Hash}
		if f.Hash != "" {
			for _, kv := range []struct{ kind, key, value string }{
				{KindDate, keyDate, f.FileDate}, {KindRetention, keyRetention, f.Retention},
			} {
				if err := pb.decide(ctx, s, f.FileID, p, kv.kind, kv.key, kv.value, "", f.Hash, now); err != nil {
					return nil, err
				}
			}
		}
		for _, t := range tagsOf[f.FileID] {
			if err := pb.decide(ctx, s, f.FileID, p, KindTag, tagKey(t), t.String(), t.String(), f.Hash,
				now); err != nil {
				return nil, err
			}
		}
	}
	if err := pb.publishSettings(ctx, s, me, now); err != nil {
		return nil, err
	}
	cur, err := s.Cursors(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.Publish(ctx, []store.SyncedChange{pb.ch}, cur); err != nil {
		return nil, err
	}
	for _, sv := range pb.seen {
		if err := s.SetSeen(ctx, sv.id, sv.key, sv.value); err != nil {
			return nil, err
		}
	}
	if err := pb.write(); err != nil {
		return []string{sd.Abs() + ": " + err.Error() + " (written back on the next run)"}, nil
	}
	return nil, nil
}
