// Package reconcile runs a scan: it walks a root (or a scope inside it), compares what it finds with the register,
// hashes only what needs it, works out what is new, changed, moved and missing, and applies it in one transaction.
// It is read-only on the user's files.
package reconcile

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alansav/filo/internal/hasher"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/walk"
)

// Event kinds.
const (
	Moved        = "moved"
	Missing      = "missing"
	Changed      = "changed"
	PossibleMove = "possible_move"
	Error        = "error"
	OtherFS      = "other_fs"
	// SameAsDeleted is a hint: a new file has the content of a file filo deleted (restored from a backup, say). It
	// stays a new file that needs a retention; the old one's is never carried over.
	SameAsDeleted = store.EventSameAsDeleted
)

// DefaultWorkers is the hashing parallelism when Options.Workers is 0.
const DefaultWorkers = 4

// SampleSize is how many events of each kind a Summary keeps.
const SampleSize = 10

// ErrEmptyWalk means a root on a mounted filesystem walked empty while the register knows files there.
var ErrEmptyWalk = errors.New("scan found no files")

// Options control one scan.
type Options struct {
	Target     string    // directory to scan; must be inside a root, whose settings (hidden, ignore) the scan uses
	Workers    int       // hashing workers; 0 = DefaultWorkers
	AllowEmpty bool      // record an empty walk on a root on a mounted filesystem anyway
	Progress   *Progress // optional, for a progress display
}

// Phases of a scan, as reported by Progress.
const (
	PhaseIdle int32 = iota
	PhaseWalking
	PhaseHashing
	PhaseWriting
)

// Progress reports a running scan. Every field is safe to read while the scan runs.
type Progress struct {
	Phase     atomic.Int32
	Walked    atomic.Int64 // directory entries listed
	HashFiles atomic.Int64 // files to hash in this scan
	HashBytes atomic.Int64 // bytes to hash (sizes from the walk)
	Hash      hasher.Progress
	mu        sync.Mutex
	root      string
}

// Root is the root being scanned.
func (p *Progress) Root() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.root
}

func (p *Progress) start(root string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.root = root
	p.mu.Unlock()
	p.Walked.Store(0)
	p.HashFiles.Store(0)
	p.HashBytes.Store(0)
	p.Hash.Files.Store(0)
	p.Hash.Bytes.Store(0)
	p.Phase.Store(PhaseWalking)
}

func (p *Progress) phase(ph int32) {
	if p != nil {
		p.Phase.Store(ph)
	}
}

// Summary is what a scan did.
type Summary struct {
	Root, Scope   string
	ScanID        int64
	Seen          int
	New           int
	Changed       int
	Moved         int
	Missing       int
	PossibleMoves int
	SameAsDeleted int // new files with the content of a file filo deleted
	Errors        int
	Samples       map[string][]store.Event // first SampleSize events of each kind
	OtherFS       []string
	FailedDirs    []string
}

// RootResult is one root's outcome in ScanAll.
type RootResult struct {
	Root    store.Root
	Summary Summary
	Err     error
}

// ScanAll scans every root in turn, each in its own transaction. A root that fails (for example an unmounted disk)
// is reported in its result and the others still run. Only cancellation stops it early.
func ScanAll(ctx context.Context, s *store.Store, opt Options) ([]RootResult, error) {
	roots, err := s.Roots(ctx)
	if err != nil {
		return nil, err
	}
	var out []RootResult
	for _, r := range roots {
		o := opt
		o.Target = r.Path
		sum, err := Scan(ctx, s, o)
		out = append(out, RootResult{Root: r, Summary: sum, Err: err})
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
	}
	return out, nil
}

type newItem struct {
	e          walk.Entry
	hash       string
	size       int64
	mtimeNS    int64
	ctimeNS    int64
	ino        uint64
	skip       bool // vanished before it could be hashed: not recorded
	unreadable bool // could not be opened or read: recorded without a hash and flagged
}

// Scan scans one target. On any error nothing is written.
func Scan(ctx context.Context, s *store.Store, opt Options) (Summary, error) {
	started := time.Now()
	workers := opt.Workers
	if workers == 0 {
		workers = DefaultWorkers
	}

	root, scope, err := s.Resolve(ctx, opt.Target)
	if err != nil {
		return Summary{}, err
	}
	knownList, err := s.Known(ctx, root.ID)
	if err != nil {
		return Summary{}, err
	}
	slices.SortFunc(knownList, func(a, b store.Known) int { return cmp.Compare(a.Path, b.Path) })
	known := make(map[string]store.Known, len(knownList))
	for _, k := range knownList {
		known[k.Path] = k
	}

	pr := opt.Progress
	pr.start(root.Path)
	defer pr.phase(PhaseIdle)
	var walked *atomic.Int64
	if pr != nil {
		walked = &pr.Walked
	}
	db := s.DBPath()
	res, err := walk.Walk(ctx, walk.Options{
		Root: root.Path, Scope: scope, IncludeHidden: root.IncludeHidden, Ignore: root.Ignore,
		Exclude: []string{db, db + "-wal", db + "-shm", db + "-journal", db + ".lock", store.SnapshotDir(db)},
		Seen:    walked,
	})
	if err != nil {
		return Summary{}, err
	}

	if len(res.Entries) == 0 && root.MountPath != "" && !opt.AllowEmpty {
		covered := 0
		for _, k := range knownList {
			if res.Covers(k.Path) {
				covered++
			}
		}
		if covered > 0 {
			return Summary{}, fmt.Errorf("%w in %s, which is on the filesystem mounted at %s, but %d files are known "+
				"there; is the right disk mounted? If they really are gone, use --allow-empty",
				ErrEmptyWalk, filepath.Join(root.Path, filepath.FromSlash(scope)), root.MountPath, covered)
		}
	}

	var ch store.Changes
	event := func(e store.Event) { ch.Events = append(ch.Events, e) }
	sum := Summary{Root: root.Path, Scope: scope, Seen: len(res.Entries), OtherFS: res.OtherFS, FailedDirs: res.FailedDirs}

	// Classify by path.
	var news []newItem
	type cand struct {
		e walk.Entry
		k store.Known
	}
	var cands []cand
	var gone []store.Known // missing in this scan, including paths whose kind changed
	seen := make(map[string]bool, len(res.Entries))
	for _, e := range res.Entries {
		seen[e.Path] = true
		k, ok := known[e.Path]
		switch {
		case !ok:
			news = append(news, newItem{e: e, size: e.Size, mtimeNS: e.MtimeNS, ctimeNS: e.CtimeNS, ino: e.Ino})
		case k.Kind != string(e.Kind):
			gone = append(gone, k)
			news = append(news, newItem{e: e, size: e.Size, mtimeNS: e.MtimeNS, ctimeNS: e.CtimeNS, ino: e.Ino})
		case e.Kind == walk.KindSymlink:
			// A symlink's ctime is recorded but not compared: it has no content to read.
			u := store.FileUpdate{FileID: k.FileID, Size: e.Size, MtimeNS: e.MtimeNS, CtimeNS: e.CtimeNS,
				LinkTarget: e.LinkTarget, LinkBroken: e.LinkBroken}
			if k.LinkTarget == e.LinkTarget && k.LinkBroken == e.LinkBroken {
				if k.Size != e.Size || k.MtimeNS != e.MtimeNS {
					ch.Updates = append(ch.Updates, u)
				}
				continue
			}
			ch.Updates = append(ch.Updates, u)
			event(store.Event{Kind: Changed, FileID: k.FileID, Path: e.Path,
				Detail: symlinkDetail(k.LinkTarget, k.LinkBroken, e.LinkTarget, e.LinkBroken)})
			sum.Changed++
		default:
			if k.Size == e.Size && k.MtimeNS == e.MtimeNS && sameInode(k.Ino, e.Ino) && sameCtime(k.CtimeNS, e.CtimeNS) &&
				k.Hash != "" && k.Unreadable == 0 {
				if k.CtimeNS == 0 && e.CtimeNS != 0 {
					// No ctime on record (a database from before ctimes were kept): record it quietly.
					ch.Updates = append(ch.Updates, store.FileUpdate{FileID: k.FileID, Size: k.Size, MtimeNS: k.MtimeNS,
						CtimeNS: e.CtimeNS, Hash: k.Hash, Ino: k.Ino})
				}
				continue // unchanged: nothing else to write
			}
			cands = append(cands, cand{e: e, k: k})
		}
	}
	for _, k := range knownList {
		if !seen[k.Path] && res.Covers(k.Path) {
			gone = append(gone, k)
		}
	}

	// Hash candidates and new regular files.
	var jobs []hasher.Job
	abs := func(rel string) string { return filepath.Join(root.Path, filepath.FromSlash(rel)) }
	for _, c := range cands {
		jobs = append(jobs, hasher.Job{Path: abs(c.e.Path)})
	}
	var newFileIdx []int // indexes into news of the regular files, in job order
	for i, n := range news {
		if n.e.Kind == walk.KindFile {
			jobs = append(jobs, hasher.Job{Path: abs(n.e.Path)})
			newFileIdx = append(newFileIdx, i)
		}
	}
	var hp *hasher.Progress
	if pr != nil {
		var total int64
		for _, c := range cands {
			total += c.e.Size
		}
		for _, i := range newFileIdx {
			total += news[i].e.Size
		}
		pr.HashFiles.Store(int64(len(jobs)))
		pr.HashBytes.Store(total)
		pr.phase(PhaseHashing)
		hp = &pr.Hash
	}
	outs, err := hasher.HashAll(ctx, jobs, workers, hp)
	if err != nil {
		return Summary{}, err
	}
	// Candidates whose content changed are decided after pairing: the new content may be a gone file that moved in.
	type changedCand struct {
		k    store.Known
		path string
		o    hasher.Outcome
	}
	var changedCands []changedCand
	for i, c := range cands {
		o := outs[i]
		if o.Err != nil {
			// The file is there but could not be hashed. Leave its row alone: its size, mtime or ctime still differ
			// from the register, so the next scan tries again. If it could not be read at all, flag it.
			event(store.Event{Kind: Error, FileID: c.k.FileID, Path: c.e.Path, Detail: "hash: " + o.Err.Error()})
			if hasher.Unreadable(o.Err) {
				ch.Unreadable = append(ch.Unreadable, c.k.FileID)
			}
			continue
		}
		if c.k.Hash != "" && c.k.Hash != string(o.Sum) {
			changedCands = append(changedCands, changedCand{k: c.k, path: c.e.Path, o: o})
			continue
		}
		// Same content (only size/mtime moved), or a hash recorded for the first time: a quiet update.
		ch.Updates = append(ch.Updates, store.FileUpdate{FileID: c.k.FileID, Size: o.Size, MtimeNS: o.MtimeNS,
			CtimeNS: o.CtimeNS, Hash: string(o.Sum), Ino: o.Ino})
	}
	for j, i := range newFileIdx {
		o := outs[len(cands)+j]
		n := &news[i]
		switch {
		case o.Err == nil:
			n.hash, n.size, n.mtimeNS, n.ctimeNS, n.ino = string(o.Sum), o.Size, o.MtimeNS, o.CtimeNS, o.Ino
		case errors.Is(o.Err, fs.ErrNotExist):
			n.skip = true
			event(store.Event{Kind: Error, Path: n.e.Path, Detail: "vanished during the scan"})
		default:
			// Recorded without a hash; known files with no hash are hashed again on every scan.
			n.unreadable = hasher.Unreadable(o.Err)
			event(store.Event{Kind: Error, Path: n.e.Path, Detail: "hash: " + o.Err.Error()})
		}
	}

	// Pairing, in three passes, each under the rules in pair():
	//  A. files gone in this scan  <->  new files, and the new content of changed files (a gone file moved onto a
	//     path whose old file moved away or was overwritten);
	//  B. files displaced in A (their path now holds another file)  <->  new files left over;
	//  C. files that went missing in earlier scans  <->  new files left over (a file that came back).
	// A changed file whose new content matches nothing stays a plain edit.
	pairedNew := map[int]bool{}  // index into news
	pairedGone := map[int]bool{} // index into gone
	moveTo := func(fileID int64, toPath string, n newItem, otherPath, detail string) {
		ch.Open = append(ch.Open, store.OpenPath{FileID: fileID, Path: toPath})
		ch.Updates = append(ch.Updates, store.FileUpdate{FileID: fileID, Size: n.size, MtimeNS: n.mtimeNS,
			CtimeNS: n.ctimeNS, Hash: n.hash, Ino: n.ino})
		event(store.Event{Kind: Moved, FileID: fileID, Path: toPath, OtherPath: otherPath, Detail: detail})
		sum.Moved++
	}
	newSides := func() []side {
		var out []side
		for j, n := range news {
			if n.e.Kind == walk.KindFile && !n.skip && !pairedNew[j] {
				out = append(out, side{ID: j, Hash: n.hash, Size: n.size, Name: path.Base(n.e.Path)})
			}
		}
		return out
	}

	// Pass A. Changed files' new content gets IDs after the news.
	var olds []side
	for i, g := range gone {
		if g.Kind == string(walk.KindFile) {
			olds = append(olds, side{ID: i, Hash: g.Hash, Size: g.Size, Name: path.Base(g.Path)})
		}
	}
	sidesA := newSides()
	for i, c := range changedCands {
		sidesA = append(sidesA, side{ID: len(news) + i, Hash: string(c.o.Sum), Size: c.o.Size, Name: path.Base(c.path)})
	}
	var displaced []changedCand
	displacedSet := map[int]bool{}
	for _, p := range pair(olds, sidesA) {
		g := gone[p[0]]
		pairedGone[p[0]] = true
		ch.Close = append(ch.Close, g.PathID)
		if p[1] < len(news) {
			pairedNew[p[1]] = true
			moveTo(g.FileID, news[p[1]].e.Path, news[p[1]], g.Path, "")
			continue
		}
		c := changedCands[p[1]-len(news)]
		displacedSet[p[1]-len(news)] = true
		displaced = append(displaced, c)
		ch.Close = append(ch.Close, c.k.PathID) // closed before any path is opened, so g can take it
		moveTo(g.FileID, c.path, newItem{hash: string(c.o.Sum), size: c.o.Size, mtimeNS: c.o.MtimeNS,
			ctimeNS: c.o.CtimeNS, ino: c.o.Ino}, g.Path, "")
	}
	for i, c := range changedCands {
		if displacedSet[i] {
			continue
		}
		ch.Updates = append(ch.Updates, store.FileUpdate{FileID: c.k.FileID, Size: c.o.Size, MtimeNS: c.o.MtimeNS,
			CtimeNS: c.o.CtimeNS, Hash: string(c.o.Sum), Ino: c.o.Ino})
		event(store.Event{Kind: Changed, FileID: c.k.FileID, Path: c.path, Detail: "content"})
		sum.Changed++
	}

	// Pass B.
	var dispSides []side
	for i, c := range displaced {
		dispSides = append(dispSides, side{ID: i, Hash: c.k.Hash, Size: c.k.Size, Name: path.Base(c.path)})
	}
	pairedDisp := map[int]bool{}
	for _, p := range pair(dispSides, newSides()) {
		c := displaced[p[0]]
		pairedDisp[p[0]], pairedNew[p[1]] = true, true
		moveTo(c.k.FileID, news[p[1]].e.Path, news[p[1]], c.path, "")
	}

	// Pass C.
	var hashes []string
	seenHash := map[string]bool{}
	for j, n := range news {
		if !pairedNew[j] && n.hash != "" && n.size > 0 && !seenHash[n.hash] {
			seenHash[n.hash] = true
			hashes = append(hashes, n.hash)
		}
	}
	earlier, err := s.MissingByHash(ctx, hashes)
	if err != nil {
		return Summary{}, err
	}
	if len(earlier) > 0 {
		roots, err := s.Roots(ctx)
		if err != nil {
			return Summary{}, err
		}
		rootPaths := map[int64]string{}
		for _, r := range roots {
			rootPaths[r.ID] = r.Path
		}
		var earlySides []side
		for i, g := range earlier {
			earlySides = append(earlySides, side{ID: i, Hash: g.Hash, Size: g.Size, Name: path.Base(g.LastPath)})
		}
		for _, p := range pair(earlySides, newSides()) {
			g := earlier[p[0]]
			pairedNew[p[1]] = true
			detail := fmt.Sprintf("returned (missing since scan %d)", g.MissingSince)
			if g.LastRootID != root.ID {
				detail += " from root " + rootPaths[g.LastRootID]
			}
			moveTo(g.FileID, news[p[1]].e.Path, news[p[1]], g.LastPath, detail)
		}
	}

	// Leftovers: gone and displaced files are missing, new files are new.
	type leftover struct {
		fileID     int64
		path, hash string
	}
	var leftGone []leftover
	missing := func(fileID int64, p, hash string) {
		event(store.Event{Kind: Missing, FileID: fileID, Path: p})
		sum.Missing++
		leftGone = append(leftGone, leftover{fileID, p, hash})
	}
	for i, g := range gone {
		if !pairedGone[i] {
			ch.Close = append(ch.Close, g.PathID)
			missing(g.FileID, g.Path, g.Hash)
		}
	}
	for i, c := range displaced {
		if !pairedDisp[i] {
			missing(c.k.FileID, c.path, c.k.Hash) // its path was closed in pass A
		}
	}
	var leftOld, leftNew []side
	for i, g := range leftGone {
		leftOld = append(leftOld, side{ID: i, Name: path.Base(g.path)})
	}
	for j, n := range news {
		if pairedNew[j] || n.skip {
			continue
		}
		ch.New = append(ch.New, store.NewFile{Path: n.e.Path, Kind: string(n.e.Kind), Size: n.size, MtimeNS: n.mtimeNS,
			CtimeNS: n.ctimeNS, Hash: n.hash, Ino: n.ino, LinkTarget: n.e.LinkTarget, LinkBroken: n.e.LinkBroken,
			Unreadable: n.unreadable})
		sum.New++
		leftNew = append(leftNew, side{ID: j, Name: path.Base(n.e.Path)})
	}

	// A new file with the content of a file filo deleted: a hint only.
	var newHashes []string
	hashSeen := map[string]bool{}
	for _, n := range ch.New {
		if n.Kind == string(walk.KindFile) && n.Hash != "" && n.Size > 0 && !hashSeen[n.Hash] {
			hashSeen[n.Hash] = true
			newHashes = append(newHashes, n.Hash)
		}
	}
	deleted, err := s.DeletedByHash(ctx, newHashes)
	if err != nil {
		return Summary{}, err
	}
	for _, n := range ch.New {
		if when, ok := deleted[n.Hash]; ok && n.Kind == string(walk.KindFile) && n.Size > 0 {
			event(store.Event{Kind: SameAsDeleted, Path: n.Path,
				Detail: "same content as a file filo deleted on " + localDate(when) + "; a new file, it needs a retention"})
			sum.SameAsDeleted++
		}
	}

	// Same name on both sides, unique on each: reported, never applied.
	for _, p := range possibleMoves(leftOld, leftNew) {
		g, n := leftGone[p[0]], news[p[1]]
		detail := "same name"
		if g.hash != "" && n.hash != "" && g.hash != n.hash {
			detail = "same name, different content"
		}
		event(store.Event{Kind: PossibleMove, FileID: g.fileID, Path: n.e.Path, OtherPath: g.path, Detail: detail})
		sum.PossibleMoves++
	}

	for _, p := range res.Problems {
		event(store.Event{Kind: Error, Path: p.Path, Detail: p.Err.Error()})
	}
	for _, d := range res.OtherFS {
		event(store.Event{Kind: OtherFS, Path: d, Detail: "on another filesystem; not scanned"})
	}
	for _, e := range ch.Events {
		if e.Kind == Error {
			sum.Errors++
		}
	}

	rec := store.ScanRecord{
		RootID: root.ID, Scope: scope, IncludeHidden: root.IncludeHidden, Ignores: root.Ignore, Started: started,
		FilesSeen: sum.Seen, NewCount: sum.New, ChangedCount: sum.Changed, MovedCount: sum.Moved,
		MissingCount: sum.Missing, ErrorCount: sum.Errors,
	}
	pr.phase(PhaseWriting)
	if sum.ScanID, err = s.ApplyScan(ctx, rec, ch); err != nil {
		return Summary{}, err
	}
	sum.Samples = map[string][]store.Event{}
	for _, e := range ch.Events {
		if len(sum.Samples[e.Kind]) < SampleSize {
			sum.Samples[e.Kind] = append(sum.Samples[e.Kind], e)
		}
	}
	return sum, nil
}

// localDate shows an RFC 3339 UTC time as a local date, or the text as it is if it does not parse.
func localDate(utc string) string {
	t, err := time.Parse(time.RFC3339Nano, utc)
	if err != nil {
		return utc
	}
	return t.Local().Format("2006-01-02")
}

// sameCtime compares change times only when both are known: a platform without them, or a row from before they were
// kept, records 0.
func sameCtime(a, b int64) bool {
	return a == 0 || b == 0 || a == b
}

// sameInode compares inodes only when both are known: a platform without inode lookup records 0.
func sameInode(a, b uint64) bool {
	return a == 0 || b == 0 || a == b
}

// symlinkDetail describes how a symlink changed. When only the broken flag changed, the target is named once, so the
// line does not read as a retarget.
func symlinkDetail(oldTarget string, oldBroken bool, newTarget string, newBroken bool) string {
	if oldTarget == newTarget {
		if newBroken {
			return "symlink: " + newTarget + " (now broken)"
		}
		return "symlink: " + newTarget + " (no longer broken)"
	}
	return "symlink: " + linkText(oldTarget, oldBroken) + " -> " + linkText(newTarget, newBroken)
}

func linkText(target string, broken bool) string {
	if broken {
		return target + " (broken)"
	}
	return target
}
