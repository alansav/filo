package share

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/retention"
	"github.com/alansav/filo/internal/store"
)

// The decisions that are not about one file (docs/design.md, Sharing decisions between devices, What is shared):
// policies (global: in every synced directory's journal), the rules inside a synced directory, the directories
// excluded from duplicates, the contents always kept, the sets of records, and which device writes a tag index. Each
// is one key with a value; a line of kind "setting" decides one, naming what it replaces, like a file decision. What
// this device holds is compared with what is in effect, and a difference is written (the value "" for gone). A
// policy moved (renamed, or merged into another) is a fact, "policy-move", applied as it is read.
const (
	KindSetting    = "setting"
	KindPolicyMove = "policy-move"
)

// Key prefixes of the settings.
const (
	keyPolicy   = "policy:"   // policy:NAME, its value ("-" for none yet)
	keyRule     = "rule:"     // rule:DIR//PATTERN (lower case), "POLICY PATTERN"
	keyExcluded = "excluded:" // excluded:DIR ("" for the whole synced directory), "1"
	keyAlways   = "always:"   // always:HASH, "1"
	keyRecords  = "records:"  // records:HASH, its records' paths, one a line, sorted
	keyIndex    = "index:"    // index:DIR//NAME, the device that writes it ("-": none, in a conflict)
)

// noValue is a policy's value when it has none yet: "" means the policy is gone.
const noValue = "-"

// SettingBody is a setting line's body.
type SettingBody struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Replaces []Ref  `json:"replaces"`
}

// PolicyMoveBody is a policy-move line's: the policy and the one it was moved to (a new name, or another policy).
type PolicyMoveBody struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// relIn is p (relative to sd's root) relative to sd's top, and whether it is inside sd.
func relIn(sd store.SyncedDir, rootID int64, p string) (string, bool) {
	if !sd.Contains(rootID, p) {
		return "", false
	}
	return strings.TrimPrefix(strings.TrimPrefix(p, sd.Dir), "/"), true
}

// localSettings is what this device holds for synced directory sd, by key. holds says, for a key not in local,
// whether that is a removal of this device's to write (a content always kept with no copy inside sd is not gone, nor
// an index another device writes that this one forgot).
func localSettings(ctx context.Context, s *store.Store, me store.Device, sd store.SyncedDir) (map[string]string,
	func(key, seen string, me store.Device) bool, error) {
	local := map[string]string{}
	pols, err := s.Policies(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range pols {
		v := p.Value
		if v == "" {
			v = noValue
		}
		local[keyPolicy+p.Name] = v
	}
	rules, err := s.Rules(ctx, sd.RootID, sd.Dir)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rules {
		if rel, ok := relIn(sd, r.RootID, r.Dir); ok {
			local[keyRule+rel+"//"+strings.ToLower(r.Pattern)] = r.Policy + " " + r.Pattern
		}
	}
	ex, err := s.ExcludedDirs(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, d := range ex {
		if d.RootID != sd.RootID {
			continue
		}
		if rel, ok := relIn(sd, d.RootID, d.Path); ok {
			local[keyExcluded+rel] = "1"
		} else if d.Path == "" || strings.HasPrefix(sd.Dir, d.Path+"/") {
			local[keyExcluded] = "1" // excluded above: the whole synced directory is
		}
	}
	always, err := s.AlwaysHashes(ctx)
	if err != nil {
		return nil, nil, err
	}
	alwaysAll := map[string]bool{}
	for _, h := range always {
		alwaysAll[h] = true
	}
	files, err := s.Files(ctx, retention.Today(time.Now()), store.Filter{RootID: sd.RootID, Scope: sd.Dir})
	if err != nil {
		return nil, nil, err
	}
	rel := map[int64]string{}
	for _, f := range files {
		if r, ok := relIn(sd, sd.RootID, f.Path); ok {
			rel[f.FileID] = r
			if alwaysAll[f.Hash] {
				local[keyAlways+f.Hash] = "1"
			}
		}
	}
	members, err := s.RecordMembers(ctx)
	if err != nil {
		return nil, nil, err
	}
	sets := map[string][]string{}
	recordHashes := map[string]bool{}
	for _, m := range members {
		recordHashes[m.Hash] = true
		if r, ok := rel[m.FileID]; ok {
			sets[m.Hash] = append(sets[m.Hash], r)
		}
	}
	for h, paths := range sets {
		if len(paths) > 1 {
			sort.Strings(paths)
			local[keyRecords+h] = strings.Join(paths, "\n")
		}
	}
	ixs, err := s.TagIndexes(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, ix := range ixs {
		if r, ok := relIn(sd, ix.RootID, ix.Dir); ok {
			w := ix.Writer
			if w == "" {
				w = me.ID
			}
			local[keyIndex+r+"//"+ix.Name] = w
		}
	}
	holds := func(key, seen string, me store.Device) bool {
		switch {
		case strings.HasPrefix(key, keyAlways):
			return !alwaysAll[strings.TrimPrefix(key, keyAlways)]
		case strings.HasPrefix(key, keyRecords):
			return !recordHashes[strings.TrimPrefix(key, keyRecords)]
		case strings.HasPrefix(key, keyIndex):
			return seen == me.ID // only the device that writes an index removes it; forgetting one stays here
		}
		return true
	}
	return local, holds, nil
}

// publishSettings adds to pb a setting line for each key whose value here differs from what is in effect: a value
// this device holds, or "" for one it no longer holds. A key with a decision not applied here yet is left alone, so
// what another device decided is never answered with what this device held before it.
func (pb *pub) publishSettings(ctx context.Context, s *store.Store, me store.Device, now time.Time) error {
	local, holds, err := localSettings(ctx, s, me, pb.sd)
	if err != nil {
		return err
	}
	cur, err := s.CurrentByKey(ctx, pb.sd.ID, store.SettingsFID)
	if err != nil {
		return err
	}
	keys := map[string]bool{}
	for k := range local {
		keys[k] = true
	}
	for k := range cur {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	seenDir, err := s.Seen(ctx, pb.sd.ID)
	if err != nil {
		return err
	}
	seenPolicies, err := s.Seen(ctx, 0)
	if err != nil {
		return err
	}
	for _, k := range sorted {
		value, have := local[k]
		ds := cur[k]
		seenAll := seenDir
		if strings.HasPrefix(k, keyPolicy) {
			seenAll = seenPolicies // a policy is global: what was seen in any synced directory
		}
		seen, wasSeen := seenAll[k]
		if !have && !holds(k, seen, me) {
			continue
		}
		waiting := false
		var refs []Ref
		var replaces []store.LineRef
		same := len(ds) > 0
		for _, d := range ds {
			same = same && sameSetting(k, d.Value, value)
			waiting = waiting || !d.Applied
			refs = append(refs, Ref{Dev: d.Dev, Seq: d.Seq})
			replaces = append(replaces, store.LineRef{Dev: d.Dev, Seq: d.Seq})
		}
		switch {
		case waiting:
			continue // another device's decision, not applied here yet
		case wasSeen && sameSetting(k, seen, value):
			continue // what this device last wrote or took in: no change of its own (a conflict's safe side too)
		case !wasSeen && (same || (len(ds) == 0 && value == "")):
			continue
		}
		raw, err := pb.w.Add(KindSetting, SettingBody{Key: k, Value: value, Replaces: refs}, now)
		if err != nil {
			return err
		}
		l := pb.w.Last()
		pb.addLine(raw, l)
		pb.ch.Decisions = append(pb.ch.Decisions, store.Decision{Dev: l.Dev, Seq: l.Seq, FID: store.SettingsFID,
			Key: k, Value: value, Decided: l.Time, Current: true, Applied: true})
		pb.ch.Replaces = append(pb.ch.Replaces, replaces)
		id := pb.sd.ID
		if strings.HasPrefix(k, keyPolicy) {
			id = 0
		}
		pb.seen = append(pb.seen, seenValue{id, k, value})
	}
	return nil
}

// seenValue is a setting value to record as seen once what pb writes is committed.
type seenValue struct {
	id         int64
	key, value string
}

// sameSetting reports whether two values of setting key are one: a rule's pattern in any case.
func sameSetting(key, a, b string) bool {
	if strings.HasPrefix(key, keyRule) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// publishMoves adds a policy-move line to pb for each policy moved on this device.
func (pb *pub) publishMoves(moves []store.PolicyEvent, now time.Time) error {
	for _, m := range moves {
		raw, err := pb.w.Add(KindPolicyMove, PolicyMoveBody{From: m.Policy, To: m.New}, now)
		if err != nil {
			return err
		}
		pb.addLine(raw, pb.w.Last())
	}
	return nil
}

// settingDecisions reads the setting lines among lines (of trusted devices) into ch, and applies the policy-move
// lines at once (a move is a fact, in the order it was made). It returns the problems met.
func settingDecisions(ctx context.Context, s *store.Store, lines []journal.Line, name func(string) string,
	ch *store.SyncedChange, now time.Time, applyMoves bool) ([]string, error) {
	var problems []string
	for _, l := range lines {
		switch l.Kind {
		case KindSetting:
			var b SettingBody
			if err := l.Body(&b); err != nil || !validSettingKey(b.Key) {
				problems = append(problems, fmt.Sprintf("line %d of %s's journal is not applied: not a setting",
					l.Seq, name(l.Dev)))
				continue
			}
			var refs []store.LineRef
			for _, r := range b.Replaces {
				refs = append(refs, store.LineRef{Dev: r.Dev, Seq: r.Seq})
			}
			ch.Decisions = append(ch.Decisions, store.Decision{Dev: l.Dev, Seq: l.Seq, FID: store.SettingsFID,
				Key: b.Key, Value: b.Value, Decided: l.Time, Current: true})
			ch.Replaces = append(ch.Replaces, refs)
		case KindPolicyMove:
			var b PolicyMoveBody
			if !applyMoves || l.Body(&b) != nil {
				continue // at a join, a journal's old moves are history: what is in effect comes as settings
			}
			from, err1 := store.PolicyName(b.From)
			to, err2 := store.PolicyName(b.To)
			if err1 != nil || err2 != nil {
				continue
			}
			if _, err := s.Policy(ctx, from); errors.Is(err, store.ErrNoPolicy) {
				continue // moved here already, or never here
			} else if err != nil {
				return problems, err
			}
			before, err := s.Latest(ctx)
			if err != nil {
				return problems, err
			}
			if _, err := s.MovePolicy(ctx, from, to, now); err != nil {
				problems = append(problems, fmt.Sprintf("%s moved policy %s to %s: not done here: %v", name(l.Dev),
					from, to, err))
				continue
			}
			if err := s.MarkPolicyChangesJournal(ctx, before.PolicyChanges); err != nil {
				return problems, err
			}
		}
	}
	return problems, nil
}

func validSettingKey(k string) bool {
	if len(k) > journal.MaxPath {
		return false
	}
	// A directory in a key is relative to the synced directory's top: checked as a line's path ("" for the top).
	dirOK := func(rel string) bool { return rel == "" || journal.CheckPath(rel) == nil }
	switch {
	case strings.HasPrefix(k, keyPolicy):
		_, err := store.PolicyName(strings.TrimPrefix(k, keyPolicy))
		return err == nil
	case strings.HasPrefix(k, keyRule):
		rel, pattern, ok := strings.Cut(strings.TrimPrefix(k, keyRule), "//")
		return ok && pattern != "" && dirOK(rel)
	case strings.HasPrefix(k, keyExcluded):
		return dirOK(strings.TrimPrefix(k, keyExcluded))
	case strings.HasPrefix(k, keyIndex):
		rel, name, ok := strings.Cut(strings.TrimPrefix(k, keyIndex), "//")
		return ok && store.ValidIndexName(name) && dirOK(rel)
	case strings.HasPrefix(k, keyAlways), strings.HasPrefix(k, keyRecords):
		return true
	}
	return false
}

// settingEffective is the value in effect for a setting from its current decisions: the one they share, or in a
// conflict the safe one: a policy with no value (its files never deletable) over forever over the latest keep-until,
// and a policy kept over gone; no rule (new files stay undated); excluded, always and records kept; and no writer for
// a tag index (neither device rewrites the other's pages).
func settingEffective(key string, ds []store.Decision) (string, bool) {
	if len(ds) == 0 {
		return "", false
	}
	values := []string{}
	for _, d := range ds {
		if !slices.Contains(values, d.Value) {
			values = append(values, d.Value)
		}
	}
	if len(values) == 1 {
		return values[0], false
	}
	switch {
	case strings.HasPrefix(key, keyPolicy):
		best, bestUntil := "", ""
		for _, v := range values {
			switch {
			case v == "":
			case v == noValue:
				return noValue, true
			default:
				u, err := store.KeepUntil("2000-01-01", v)
				if err != nil {
					return noValue, true
				}
				if best == "" || later(u, bestUntil) {
					best, bestUntil = v, u
				}
			}
		}
		return best, true
	case strings.HasPrefix(key, keyRule):
		return "", true
	case strings.HasPrefix(key, keyIndex):
		return noValue, true
	}
	for _, v := range values { // excluded, always, records: kept
		if v != "" {
			return v, true
		}
	}
	return "", true
}

// applySetting gives this device the value in effect for a setting of synced directory sd. It reports whether it is
// applied (false: it waits, as records whose files are not all here yet), and a problem for the person.
func applySetting(ctx context.Context, s *store.Store, me store.Device, sd store.SyncedDir, key string,
	ds []store.Decision, now time.Time, today string) (bool, string, error) {
	done, problem, err := applySettingValue(ctx, s, me, sd, key, ds, now, today)
	if err != nil || !done {
		return done, problem, err
	}
	// What is applied is seen: the value this device holds now, unless it holds another for a reason of its own
	// (excluded above), which is then what it last took in.
	value, _ := settingEffective(key, ds)
	if local, _, err := localSettings(ctx, s, me, sd); err == nil {
		if v, ok := local[key]; ok && strings.HasPrefix(key, keyExcluded) {
			value = v
		}
	}
	id := sd.ID
	if strings.HasPrefix(key, keyPolicy) {
		id = 0
	}
	return true, problem, s.SetSeen(ctx, id, key, value)
}

func applySettingValue(ctx context.Context, s *store.Store, me store.Device, sd store.SyncedDir, key string,
	ds []store.Decision, now time.Time, today string) (bool, string, error) {
	value, conflict := settingEffective(key, ds)
	switch {
	case strings.HasPrefix(key, keyPolicy):
		return applyPolicy(ctx, s, strings.TrimPrefix(key, keyPolicy), value, now)
	case strings.HasPrefix(key, keyRule):
		rel, pattern, _ := strings.Cut(strings.TrimPrefix(key, keyRule), "//")
		dir := joinDir(sd.Dir, rel)
		policy, spelled, _ := strings.Cut(value, " ")
		if spelled == "" {
			spelled = pattern
		}
		if value == "" {
			_, err := s.RemoveRule(ctx, sd.RootID, dir, spelled)
			if err != nil && !errors.Is(err, store.ErrNoRule) {
				return true, fmt.Sprintf("rule %s in %s not removed here: %v", spelled, dir, err), nil
			}
			return true, "", nil
		}
		if _, _, err := s.SetRule(ctx, sd.RootID, dir, spelled, policy, now); errors.Is(err, store.ErrNoRule) {
			if _, _, _, err := s.AddRule(ctx, sd.RootID, dir, spelled, policy, today, now); err != nil {
				return true, fmt.Sprintf("rule %s in %s not added here: %v", spelled, dir, err), nil
			}
		} else if err != nil {
			return true, fmt.Sprintf("rule %s in %s not set here: %v", spelled, dir, err), nil
		}
		return true, "", nil
	case strings.HasPrefix(key, keyExcluded):
		dir := joinDir(sd.Dir, strings.TrimPrefix(key, keyExcluded))
		if value == "" {
			if at, ok, err := s.Exclusion(ctx, sd.RootID, dir); err != nil {
				return false, "", err
			} else if ok && at != dir {
				return true, fmt.Sprintf("%s was included on another device, and stays excluded here: %s is "+
					"excluded (filo duplicates include it to share that)", dir, at), nil
			}
			_, err := s.Include(ctx, sd.RootID, dir)
			return err == nil, "", err
		}
		if _, ok, err := s.Exclusion(ctx, sd.RootID, dir); err != nil || ok {
			return true, "", err // excluded already, here or above
		}
		_, err := s.Exclude(ctx, sd.RootID, dir, now)
		return err == nil, "", err
	case strings.HasPrefix(key, keyAlways):
		h := strings.TrimPrefix(key, keyAlways)
		if value == "" {
			return true, "", s.RemoveAlways(ctx, h)
		}
		return true, "", s.AddAlways(ctx, h, now)
	case strings.HasPrefix(key, keyRecords):
		return applyRecords(ctx, s, sd, strings.TrimPrefix(key, keyRecords), value, now, today)
	case strings.HasPrefix(key, keyIndex):
		rel, name, _ := strings.Cut(strings.TrimPrefix(key, keyIndex), "//")
		problem := ""
		if conflict {
			problem = fmt.Sprintf("index %s in %s: two devices write it; neither does until one takes it (filo "+
				"indexes take)", name, joinDir(sd.Dir, rel))
		}
		return applyIndex(ctx, s, me, sd, joinDir(sd.Dir, rel), name, value, now, problem)
	}
	return true, "", nil
}

func joinDir(top, rel string) string {
	switch {
	case top == "":
		return rel
	case rel == "":
		return top
	}
	return top + "/" + rel
}

// applyPolicy gives policy name the value value ("" gone, noValue none yet): made if missing, set if different,
// removed if gone and no file or rule here uses it (else said, and kept).
func applyPolicy(ctx context.Context, s *store.Store, name, value string, now time.Time) (bool, string, error) {
	n, err := store.PolicyName(name)
	if err != nil {
		return true, "", nil
	}
	p, err := s.Policy(ctx, n)
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrNoPolicy) {
		return false, "", err
	}
	v := value
	if v == noValue {
		v = ""
	}
	before, err := s.Latest(ctx)
	if err != nil {
		return false, "", err
	}
	problem := ""
	switch {
	case value == "" && exists:
		if err := s.RemovePolicy(ctx, n, now); errors.Is(err, store.ErrPolicyInUse) {
			problem = fmt.Sprintf("policy %s was removed on another device, and is kept here: files or rules here "+
				"use it", n)
		} else if err != nil {
			return false, "", err
		}
	case value == "":
	case !exists:
		if err := s.AddPolicy(ctx, n, v, now); err != nil && !errors.Is(err, store.ErrPolicyExists) {
			return true, fmt.Sprintf("policy %s not added here with %s: %v", n, v, err), nil
		}
	case p.Value != v:
		if _, err := s.SetPolicy(ctx, n, v, now); err != nil {
			return true, fmt.Sprintf("policy %s not set to %s here: %v", n, v, err), nil
		}
	}
	return true, problem, s.MarkPolicyChangesJournal(ctx, before.PolicyChanges)
}

// applyRecords keeps the files at paths (relative to sd's top, one a line) with content hash as one set of records,
// or, for "", ends any set of hash inside sd. It waits until every file is here.
func applyRecords(ctx context.Context, s *store.Store, sd store.SyncedDir, hash, value string, now time.Time,
	today string) (bool, string, error) {
	if value == "" {
		members, err := s.RecordMembers(ctx)
		if err != nil {
			return false, "", err
		}
		done := map[int64]bool{}
		for _, m := range members {
			if m.Hash == hash && !done[m.Set] {
				done[m.Set] = true
				if err := s.RemoveRecordSet(ctx, m.Set); err != nil {
					return false, "", err
				}
			}
		}
		return true, "", nil
	}
	var ids []int64
	for _, p := range strings.Split(value, "\n") {
		id, err := s.MatchFile(ctx, sd, hash, p)
		if err != nil {
			return false, "", err
		}
		if id == 0 {
			return false, "", nil
		}
		ids = append(ids, id)
	}
	res, err := s.SetFiles(ctx, ids, store.SetValues{Duplicates: store.DupRecords}, now, today,
		store.SetOptions{Command: store.CommandJournal, Confirm: func(store.SetResult) bool { return true }})
	if err != nil {
		return false, "", err
	}
	if len(res.Refusals) > 0 {
		return true, fmt.Sprintf("records of %s not kept here: %s", res.Refusals[0].Path, res.Refusals[0].Reason), nil
	}
	return true, "", nil
}

// applyIndex records which device writes index name in directory dir: it is declared here if it is not (its pages
// written by that device only), and forgotten when gone and written by another device.
func applyIndex(ctx context.Context, s *store.Store, me store.Device, sd store.SyncedDir, dir, name, writer string,
	now time.Time, problem string) (bool, string, error) {
	ixs, err := s.TagIndexes(ctx)
	if err != nil {
		return false, "", err
	}
	var ix *store.TagIndex
	for i := range ixs {
		if ixs[i].RootID == sd.RootID && ixs[i].Dir == dir && strings.EqualFold(ixs[i].Name, name) {
			ix = &ixs[i]
		}
	}
	w := writer
	if w == me.ID {
		w = ""
	}
	switch {
	case writer == "" && ix != nil && ix.Writer != "":
		return true, problem, s.RemoveTagIndex(ctx, ix.ID)
	case writer == "":
		return true, problem, nil
	case ix == nil:
		if !store.ValidIndexName(name) {
			return true, problem, nil
		}
		nix, err := s.AddTagIndex(ctx, sd.RootID, dir, name, now)
		if err != nil {
			return true, fmt.Sprintf("index %s in %s not declared here: %v", name, dir, err), nil
		}
		return true, problem, s.SetIndexWriter(ctx, nix.ID, w)
	case ix.Writer != w:
		return true, problem, s.SetIndexWriter(ctx, ix.ID, w)
	}
	return true, problem, nil
}
