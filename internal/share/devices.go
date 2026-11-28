package share

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alansav/filo/internal/journal"
	"github.com/alansav/filo/internal/store"
)

// ErrNoDevice is a device name or ID that names no device met in a journal; ErrAmbiguous one that names several.
var (
	ErrNoDevice  = errors.New("no device of that name or ID")
	ErrAmbiguous = errors.New("more than one device has that name or an ID starting so")
)

// FindDevice finds a known device by its name (any case) or the start of its ID.
func FindDevice(ctx context.Context, s *store.Store, arg string) (store.KnownDevice, error) {
	list, err := s.KnownDevices(ctx)
	if err != nil {
		return store.KnownDevice{}, err
	}
	var hits []store.KnownDevice
	for _, d := range list {
		if strings.EqualFold(d.Name, arg) {
			hits = append(hits, d)
		}
	}
	if len(hits) == 0 && len(arg) >= 4 {
		for _, d := range list {
			if strings.HasPrefix(d.Dev, strings.ToLower(arg)) {
				hits = append(hits, d)
			}
		}
	}
	switch len(hits) {
	case 0:
		return store.KnownDevice{}, fmt.Errorf("%s: %w (filo devices lists them)", arg, ErrNoDevice)
	case 1:
		return hits[0], nil
	}
	return store.KnownDevice{}, fmt.Errorf("%s: %w: give more of its ID (filo devices lists them)", arg, ErrAmbiguous)
}

// Where returns the joined synced directories where device dev has a journal (as the register knows it, or on the
// disk now).
func Where(ctx context.Context, s *store.Store, dev string) ([]store.SyncedDir, error) {
	dirs, err := s.SyncedDirs(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.SyncedDir
	for _, sd := range dirs {
		if sd.State != store.SyncedJoined {
			continue
		}
		if hasJournal(ctx, s, sd, dev) {
			out = append(out, sd)
		}
	}
	return out, nil
}

func hasJournal(ctx context.Context, s *store.Store, sd store.SyncedDir, dev string) bool {
	if js, err := s.Journals(ctx, sd.ID); err == nil {
		for _, j := range js {
			if j.Dev == dev {
				return true
			}
		}
	}
	d, err := journal.Open(sd.Abs())
	if err != nil {
		return false
	}
	defer d.Close()
	ok, _ := d.Exists(journal.JournalName(dev))
	return ok
}

// Trust trusts device kd (met in a journal, not trusted), and vouches for it with a trust line in this device's
// journal in each synced directory where kd has one (in), so the other devices trust it too.
func Trust(ctx context.Context, s *store.Store, kd store.KnownDevice, in []store.SyncedDir, now time.Time) error {
	if kd.Trusted() {
		return fmt.Errorf("%s is trusted already", kd.Name)
	}
	me, err := s.Device(ctx)
	if err != nil {
		return err
	}
	kd.TrustedUTC, kd.VouchedBy = timeText(now), ""
	if _, err := s.ApplySynced(ctx, store.SyncedChange{Devices: []store.KnownDevice{kd}}); err != nil {
		return err
	}
	var errs []error
	for _, sd := range in {
		if err := AppendOwn(ctx, s, me, sd, KindTrust, TrustBody{Device: kd.Dev, Key: kd.Key, Name: kd.Name},
			now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Retire retires device kd (trusted, not this device) with a retire line in this device's journal in each synced
// directory in, naming the last of kd's lines read there: those count, anything kd writes after is refused, here
// and on every device that reads the line.
func Retire(ctx context.Context, s *store.Store, kd store.KnownDevice, in []store.SyncedDir, now time.Time) error {
	me, err := s.Device(ctx)
	if err != nil {
		return err
	}
	switch {
	case kd.Dev == me.ID:
		return errors.New("this device cannot retire itself: retire it from another device")
	case kd.Retired():
		return fmt.Errorf("%s is retired already", kd.Name)
	}
	kd.RetiredUTC, kd.RetiredBy = timeText(now), me.ID
	if _, err := s.ApplySynced(ctx, store.SyncedChange{Devices: []store.KnownDevice{kd}}); err != nil {
		return err
	}
	var errs []error
	for _, sd := range in {
		js, err := s.Journals(ctx, sd.ID)
		if err != nil {
			return err
		}
		var st store.JournalState
		for _, j := range js {
			if j.Dev == kd.Dev {
				st = j
			}
		}
		last := max(st.Seq, 1)
		if _, err := s.ApplySynced(ctx, store.SyncedChange{SyncedID: sd.ID, Journals: []store.JournalUpdate{{
			Dev: kd.Dev, Seq: st.Seq, Offset: st.Offset, Problem: st.Problem, SeenUTC: st.SeenUTC,
			Applied: st.Applied, RetiredSeq: last}}}); err != nil {
			return err
		}
		if err := AppendOwn(ctx, s, me, sd, KindRetire, RetireBody{Device: kd.Dev, Last: last}, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Rename writes a name line in this device's journal in every joined synced directory, after the register renamed
// it, so the other devices show the new name.
func Rename(ctx context.Context, s *store.Store, now time.Time) error {
	me, err := s.Device(ctx)
	if err != nil {
		return err
	}
	dirs, err := s.SyncedDirs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, sd := range dirs {
		if sd.State != store.SyncedJoined {
			continue
		}
		if err := AppendOwn(ctx, s, me, sd, KindName, NameBody{Name: me.Name}, now); err != nil {
			errs = append(errs, err)
		}
	}
	kd := self(me, now)
	if list, err := s.KnownDevices(ctx); err == nil {
		for _, d := range list {
			if d.Dev == me.ID {
				kd.TrustedUTC = d.TrustedUTC
			}
		}
	}
	if _, err := s.ApplySynced(ctx, store.SyncedChange{Devices: []store.KnownDevice{kd}}); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
