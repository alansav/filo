package share

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/alansav/filo/internal/device"
	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/tags"
)

// Kinds of file decision lines (docs/design.md, Sharing decisions between devices, What is shared).
const (
	KindDate      = "date"      // a file's file_date
	KindRetention = "retention" // a file's retention: its own, or the name of its policy
	KindTag       = "tag"       // one tag on a file, added or removed
)

// Keys of the things a file decision decides: one file's date, its retention, or one of its tags.
const (
	keyDate      = "date"
	keyRetention = "retention"
	keyTagPrefix = "tag:"
)

// Ref names a line in a journal: a decision a new one replaces.
type Ref struct {
	Dev string `json:"dev"`
	Seq int64  `json:"seq"`
}

// FileBody is the body of a file decision line: the file's shared ID, its content and path (relative to the synced
// directory's top) when it was decided, the decisions it replaces, and the decision: the date or the retention ("":
// cleared), or for a tag line the tag as spelled and, as the value, the same when it is added or "" when it is
// removed.
type FileBody struct {
	File     string `json:"file"`
	Hash     string `json:"hash"`
	Path     string `json:"path"`
	Replaces []Ref  `json:"replaces"`
	Value    string `json:"value"`
	Tag      string `json:"tag,omitempty"`
	What     string `json:"what,omitempty"` // a fact line: place or deleted
}

// IsFileKind reports whether a line of kind is a file decision.
func IsFileKind(kind string) bool {
	return kind == KindDate || kind == KindRetention || kind == KindTag || kind == KindFact
}

// tagKey is the key a tag's decisions share: its key and folded value, so one tag in any case is one thing decided.
func tagKey(t tags.Tag) string { return keyTagPrefix + t.Key + ":" + t.Folded() }

// readDecision reads a file decision line of synced directory id, checking every part: a line that does not check
// is refused (an error), never applied.
func readDecision(id int64, l journal.Line) (store.Decision, []store.LineRef, error) {
	var b FileBody
	if err := l.Body(&b); err != nil {
		return store.Decision{}, nil, err
	}
	switch {
	case !device.ValidID(b.File):
		return store.Decision{}, nil, errors.New("not a file's shared ID")
	case b.Hash != "" && !validHash(b.Hash):
		return store.Decision{}, nil, errors.New("not a content hash")
	}
	if err := journal.CheckPath(b.Path); err != nil {
		return store.Decision{}, nil, err
	}
	d := store.Decision{SyncedID: id, Dev: l.Dev, Seq: l.Seq, FID: b.File, Hash: b.Hash, Path: b.Path, Value: b.Value,
		Current: true}
	switch l.Kind {
	case KindDate:
		d.Key = keyDate
		if b.Value != "" {
			if _, err := retention.ParseDate(b.Value); err != nil {
				return store.Decision{}, nil, err
			}
		}
	case KindRetention:
		d.Key = keyRetention
		if b.Value != "" && !store.IsPolicyRef(b.Value) {
			if _, err := retention.ParseRetention(b.Value); err != nil {
				return store.Decision{}, nil, err
			}
		}
		if store.IsPolicyRef(b.Value) {
			if _, err := store.PolicyName(b.Value); err != nil {
				return store.Decision{}, nil, err
			}
		}
	case KindFact:
		switch b.What {
		case "place", "deleted":
			d.Key = "fact:" + b.What
		default:
			return store.Decision{}, nil, errors.New("not a fact")
		}
	case KindTag:
		t, err := tags.Parse(b.Tag)
		if err != nil {
			return store.Decision{}, nil, err
		}
		if b.Value != "" && b.Value != t.String() {
			return store.Decision{}, nil, errors.New("a tag line whose value is not its tag")
		}
		d.Key = tagKey(t)
	default:
		return store.Decision{}, nil, fmt.Errorf("not a file decision: %s", l.Kind)
	}
	refs := make([]store.LineRef, 0, len(b.Replaces))
	for _, r := range b.Replaces {
		if !device.ValidID(r.Dev) || r.Seq < 1 {
			return store.Decision{}, nil, errors.New("a replaced line that is not one")
		}
		refs = append(refs, store.LineRef{Dev: r.Dev, Seq: r.Seq})
	}
	return d, refs, nil
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// effective is the value in effect for a key from its current decisions: the one value they share, or, in a
// conflict, the safe one, which never makes a file deletable sooner (docs/design.md, Ordering and conflicts): for a
// date or a retention, none wins (the file then needs a decision, and is never deletable), then the latest keep
// until, forever latest of all; for a tag, added wins over removed. keepUntil works a date or a retention out to
// the date the file would be kept until ("" when it cannot be: then none wins too).
func effective(key string, ds []store.Decision, keepUntil func(key, value string) string) (string, bool) {
	if len(ds) == 0 {
		return "", false
	}
	isTag := strings.HasPrefix(key, keyTagPrefix)
	conflict := false
	for _, d := range ds[1:] {
		if (!isTag && d.Value != ds[0].Value) || (isTag && (d.Value == "") != (ds[0].Value == "")) {
			conflict = true // a tag: only added against removed, not two spellings
		}
	}
	if !conflict {
		return ds[0].Value, false
	}
	if key == keyDate || key == keyRetention {
		best, bestUntil := "", ""
		for i, d := range ds {
			u := ""
			if d.Value != "" {
				u = keepUntil(key, d.Value)
			}
			if u == "" {
				return "", true
			}
			if i == 0 || later(u, bestUntil) {
				best, bestUntil = d.Value, u
			}
		}
		return best, true
	}
	for _, d := range ds {
		if d.Value != "" {
			return d.Value, true
		}
	}
	return "", true
}

// later reports whether keep-until a is later than b: forever is the latest.
func later(a, b string) bool {
	switch {
	case a == b:
		return false
	case a == store.Forever:
		return true
	case b == store.Forever:
		return false
	}
	return a > b
}
