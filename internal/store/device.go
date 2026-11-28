package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/alansav/filo/internal/device"
)

// The device's identity, in meta (docs/design.md, Sharing decisions between devices): made with the database, or by
// the upgrade to schema 14. The device is its register: a restored register is the same device.
const (
	metaDeviceID   = "device_id"
	metaDeviceName = "device_name"
	metaDeviceKey  = "device_key" // the ed25519 private key's seed, base64; never leaves the register
)

// hostname is the host name a new device is named after; a test replaces it.
var hostname = os.Hostname

// ErrDevice is a register whose device identity is missing or damaged.
var ErrDevice = errors.New("the register's device identity is missing or damaged")

// Device is this register's device: its ID, name and signing key.
type Device struct {
	ID   string
	Name string
	Key  ed25519.PrivateKey
}

// Public is the device's public key.
func (d Device) Public() ed25519.PublicKey { return d.Key.Public().(ed25519.PublicKey) }

// Fingerprint is what a person compares to check the device's key.
func (d Device) Fingerprint() string { return device.Fingerprint(d.Public()) }

// insertDevice gives the register in tx a new device identity, named after the host, unless it has one. A register
// with only part of one is refused: filling in the rest would pair a known ID with a key no other device knows.
func insertDevice(ctx context.Context, tx *sql.Tx) error {
	var have int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM meta WHERE key IN (?, ?, ?)`, metaDeviceID,
		metaDeviceName, metaDeviceKey).Scan(&have); err != nil {
		return fmt.Errorf("read the device identity: %w", err)
	}
	switch have {
	case 3:
		return nil
	case 0:
	default:
		return fmt.Errorf("%w: only part of it is there", ErrDevice)
	}
	id, err := device.NewID()
	if err != nil {
		return err
	}
	key, err := device.NewKey()
	if err != nil {
		return err
	}
	for _, kv := range [][2]string{
		{metaDeviceID, id},
		{metaDeviceName, device.DefaultName(hostname)},
		{metaDeviceKey, device.EncodeKey(key)},
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)`, kv[0], kv[1]); err != nil {
			return fmt.Errorf("write the device identity: %w", err)
		}
	}
	return nil
}

// Device returns this register's device. Each part is checked: a register edited by hand is refused, not used.
func (s *Store) Device(ctx context.Context) (Device, error) {
	read := func(key string) (string, error) {
		var v string
		err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: meta has no %s", ErrDevice, key)
		}
		if err != nil {
			return "", fmt.Errorf("read meta %s: %w", key, err)
		}
		return v, nil
	}
	var d Device
	var err error
	if d.ID, err = read(metaDeviceID); err != nil {
		return Device{}, err
	}
	if !device.ValidID(d.ID) {
		return Device{}, fmt.Errorf("%w: the device ID is not valid", ErrDevice)
	}
	if d.Name, err = read(metaDeviceName); err != nil {
		return Device{}, err
	}
	if device.CheckName(d.Name) != nil {
		return Device{}, fmt.Errorf("%w: the device name is not valid", ErrDevice)
	}
	k, err := read(metaDeviceKey)
	if err != nil {
		return Device{}, err
	}
	if d.Key, err = device.DecodeKey(k); err != nil {
		return Device{}, fmt.Errorf("%w: %v", ErrDevice, err)
	}
	return d, nil
}

// SetDeviceName renames this device (device.ErrName if name cannot be one).
func (s *Store) SetDeviceName(ctx context.Context, name string) error {
	if err := device.CheckName(name); err != nil {
		return err
	}
	res, err := s.exec(ctx, `UPDATE meta SET value = ? WHERE key = ?`, name, metaDeviceName)
	if err != nil {
		return fmt.Errorf("rename the device: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("rename the device: %w", ErrDevice)
	}
	return nil
}

// NewDeviceIdentity gives this register a new device ID and key, keeping its name: for a register copied to another
// machine, which would otherwise write as the device it was copied from. It returns the new device.
func (s *Store) NewDeviceIdentity(ctx context.Context) (Device, error) {
	if _, err := s.Device(ctx); err != nil {
		return Device{}, err
	}
	id, err := device.NewID()
	if err != nil {
		return Device{}, err
	}
	key, err := device.NewKey()
	if err != nil {
		return Device{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, err
	}
	defer tx.Rollback()
	for _, kv := range [][2]string{{metaDeviceID, id}, {metaDeviceKey, device.EncodeKey(key)}} {
		res, err := tx.ExecContext(ctx, `UPDATE meta SET value = ? WHERE key = ?`, kv[1], kv[0])
		if err != nil {
			return Device{}, fmt.Errorf("make a new device identity: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return Device{}, fmt.Errorf("make a new device identity: %w", ErrDevice)
		}
	}
	if err := s.commit(tx); err != nil {
		return Device{}, err
	}
	return s.Device(ctx)
}
