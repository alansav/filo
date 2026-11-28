package cli

import (
	"context"
	"fmt"

	"github.com/alansav/filo/internal/device"
	"github.com/alansav/filo/internal/share"
	"github.com/alansav/filo/internal/store"
	"github.com/alansav/filo/internal/view"
)

// filo device (docs/design.md, Sharing decisions between devices): this register's device. With no subcommand it
// shows the device's name, ID and key fingerprint, which another device's join prompt asks the person to compare;
// name renames it; new gives a copied register a new ID and key, after asking.

const deviceUsage = "filo device | name NAME | new [-y]"

func (e *env) device(ctx context.Context, dbBefore string, args []string) error {
	sub, rest := rootsSub(args)
	fl := e.flags("device")
	db := dbFlag(fl, dbBefore)
	yes := fl.Bool("y", false, "")
	pos, err := e.parse(fl, "device", rest)
	if err != nil {
		return err
	}
	want := map[string]int{"": 0, "name": 1, "new": 0}
	n, known := want[sub]
	switch {
	case !known:
		return usageError{fmt.Sprintf("unknown device command %q; see filo help device", sub)}
	case len(pos) != n:
		return usageError{"wrong number of arguments: " + deviceUsage}
	case *yes && sub != "new":
		return usageError{"-y is for device new"}
	}
	if sub == "name" {
		if err := device.CheckName(pos[0]); err != nil {
			return usageError{fmt.Sprintf("%s: %v", pos[0], err)}
		}
	}
	s, err := e.open(ctx, *db)
	if err != nil {
		return err
	}
	defer e.closeStore(s)
	d, err := s.Device(ctx)
	if err != nil {
		return err
	}
	switch sub {
	case "name":
		return e.deviceName(ctx, s, d, pos[0])
	case "new":
		return e.deviceNew(ctx, s, d, *yes)
	}
	e.deviceShow("This device", d)
	return nil
}

// deviceShow writes the device: its name in the heading, then its ID and fingerprint.
func (e *env) deviceShow(title string, d store.Device) {
	r := e.view
	r.Heading(title, view.Where{}, d.Name)
	r.Fields(0, []view.Field{
		{Label: "ID", Value: d.ID},
		{Label: "fingerprint", Value: d.Fingerprint(), Note: "what another device asks you to compare"},
	})
}

func (e *env) deviceName(ctx context.Context, s *store.Store, d store.Device, name string) error {
	r := e.view
	if name == d.Name {
		r.Heading("Device unchanged", view.Where{}, "already named "+name)
		return nil
	}
	if err := s.SetDeviceName(ctx, name); err != nil {
		return err
	}
	r.Heading("Device renamed", view.Where{}, d.Name+" → "+name)
	if err := share.Rename(ctx, s, now()); err != nil {
		return fmt.Errorf("the new name is not in every synced directory's journal yet: %w", err)
	}
	return nil
}

func (e *env) deviceNew(ctx context.Context, s *store.Store, d store.Device, yes bool) error {
	r := e.view
	e.deviceShow("This device", d)
	r.Blank()
	r.Paragraph(0, "A new device ID and key are for a register copied from another machine, which would "+
		"otherwise share decisions as the device it was copied from. The name stays. The other devices see "+
		"this register as a new device, to be trusted again.")
	if err := e.outputErr(); err != nil {
		return err
	}
	ok, err := e.confirmChange(ctx, "Give this register a new device ID and key?", yes, nil)
	if err != nil || !ok {
		return err
	}
	nd, err := s.NewDeviceIdentity(ctx) // its errors say what failed
	if err != nil {
		return err
	}
	if !yes {
		r.Blank()
	}
	e.deviceShow("New device identity", nd)
	return nil
}
