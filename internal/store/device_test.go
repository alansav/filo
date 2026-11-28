package store

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strconv"
	"testing"

	"github.com/alansav/filo/internal/device"
)

func TestNewDatabaseHasADevice(t *testing.T) {
	ctx := context.Background()
	old := hostname
	hostname = func() (string, error) { return "laptop.local", nil }
	defer func() { hostname = old }()
	db := tempDB(t)
	s := mustOpen(t, Options{DBPath: db})
	d, err := s.Device(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !device.ValidID(d.ID) || d.Name != "laptop" || len(d.Key) != ed25519.PrivateKeySize {
		t.Fatalf("Device = %q %q (key %d bytes), want a valid ID named laptop", d.ID, d.Name, len(d.Key))
	}
	mustClose(t, s)

	// Opening it again is the same device.
	s = mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	again, err := s.Device(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != d.ID || !again.Key.Equal(d.Key) || again.Fingerprint() != d.Fingerprint() {
		t.Fatal("the device changed between two opens")
	}

	// Two databases are two devices.
	other := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, other)
	o, err := other.Device(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.ID == d.ID || o.Key.Equal(d.Key) {
		t.Fatal("two databases have one device identity")
	}
}

func TestMigrateFromV13MakesADevice(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db, `DELETE FROM meta WHERE key IN ('device_id', 'device_name', 'device_key')`,
		`UPDATE meta SET value = '13' WHERE key = 'schema_version'`)
	s := mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	if v := readMeta(t, s)[metaSchemaVersion]; v != strconv.Itoa(SchemaVersion) {
		t.Fatalf("schema_version = %s after upgrade, want %d", v, SchemaVersion)
	}
	if _, err := s.Device(ctx); err != nil {
		t.Fatalf("Device after the upgrade: %v", err)
	}
}

func TestDeviceRefusesADamagedIdentity(t *testing.T) {
	ctx := context.Background()
	tests := []struct{ name, stmt string }{
		{"no ID", `DELETE FROM meta WHERE key = 'device_id'`},
		{"bad ID", `UPDATE meta SET value = '../x' WHERE key = 'device_id'`},
		{"bad name", `UPDATE meta SET value = 'a b' WHERE key = 'device_name'`},
		{"no key", `DELETE FROM meta WHERE key = 'device_key'`},
		{"bad key", `UPDATE meta SET value = 'AAAA' WHERE key = 'device_key'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := tempDB(t)
			mustClose(t, mustOpen(t, Options{DBPath: db}))
			rawExec(t, db, tt.stmt)
			s := mustOpen(t, Options{DBPath: db})
			defer mustClose(t, s)
			if _, err := s.Device(ctx); !errors.Is(err, ErrDevice) {
				t.Fatalf("Device = %v, want ErrDevice", err)
			}
		})
	}
}

func TestSetDeviceName(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	before, _ := s.Device(ctx)
	if err := s.SetDeviceName(ctx, "desktop"); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Device(ctx)
	if d.Name != "desktop" || d.ID != before.ID {
		t.Fatalf("after renaming: %q %q, want desktop with the same ID", d.Name, d.ID)
	}
	if !s.Changed() {
		t.Error("renaming the device is not noted as a change (no snapshot)")
	}
	if err := s.SetDeviceName(ctx, "my desktop"); !errors.Is(err, device.ErrName) {
		t.Errorf("SetDeviceName(\"my desktop\") = %v, want ErrName", err)
	}
}

func TestNewDeviceIdentity(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t, Options{DBPath: tempDB(t)})
	defer mustClose(t, s)
	if err := s.SetDeviceName(ctx, "copy"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Device(ctx)
	d, err := s.NewDeviceIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == before.ID || d.Key.Equal(before.Key) || d.Fingerprint() == before.Fingerprint() {
		t.Fatal("NewDeviceIdentity kept the ID or the key")
	}
	if d.Name != "copy" {
		t.Errorf("name = %q after a new identity, want it kept", d.Name)
	}
	if got, _ := s.Device(ctx); got.ID != d.ID {
		t.Error("the new identity is not the one stored")
	}
}

func TestMigrateRefusesAPartialDevice(t *testing.T) {
	db := tempDB(t)
	mustClose(t, mustOpen(t, Options{DBPath: db}))
	rawExec(t, db, `DELETE FROM meta WHERE key = 'device_key'`, `UPDATE meta SET value = '13' WHERE key = 'schema_version'`)
	if _, err := Open(context.Background(), Options{DBPath: db}); !errors.Is(err, ErrDevice) {
		t.Fatalf("Open with part of a device identity = %v, want ErrDevice", err)
	}
}

func TestMigrateKeepsAWholeDevice(t *testing.T) {
	ctx := context.Background()
	db := tempDB(t)
	s := mustOpen(t, Options{DBPath: db})
	before, _ := s.Device(ctx)
	mustClose(t, s)
	rawExec(t, db, `UPDATE meta SET value = '13' WHERE key = 'schema_version'`)
	s = mustOpen(t, Options{DBPath: db})
	defer mustClose(t, s)
	after, err := s.Device(ctx)
	if err != nil || after.ID != before.ID || !after.Key.Equal(before.Key) {
		t.Fatalf("the upgrade replaced a whole device identity: %v", err)
	}
}
