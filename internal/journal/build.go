package journal

import (
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/alansav/filo/internal/device"
)

// Writer makes a device's lines, each signed and following the one before. It writes nothing to the disk: the
// caller stores the lines in the register first, then appends them (Dir.Append).
type Writer struct {
	key  ed25519.PrivateKey
	last Line
}

// timeFormat is how a line's time is written: RFC 3339, UTC, to the second.
const timeFormat = time.RFC3339

// Start makes line 1 of a new journal for the device dev, named name, with key, and the Writer for what follows.
func Start(key ed25519.PrivateKey, dev, name string, now time.Time) (*Writer, []byte, error) {
	if !device.ValidID(dev) {
		return nil, nil, errors.New("not a device ID")
	}
	if err := device.CheckName(name); err != nil {
		return nil, nil, err
	}
	at := now.UTC().Format(timeFormat)
	raw, err := Encode(key, Header{Dev: dev, Seq: 1, Time: at, Kind: KindDevice},
		DeviceBody{Key: device.EncodePublic(key.Public().(ed25519.PublicKey)), Name: name, Created: at})
	if err != nil {
		return nil, nil, err
	}
	l, err := Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	return &Writer{key: key, last: l}, raw, nil
}

// Resume is the Writer that follows last, a line of the device whose key this is.
func Resume(key ed25519.PrivateKey, last Line) (*Writer, error) {
	if !last.Verify(key.Public().(ed25519.PublicKey)) {
		return nil, errors.New("the last line is not signed with this device's key")
	}
	return &Writer{key: key, last: last}, nil
}

// Last is the last line made.
func (w *Writer) Last() Line { return w.last }

// Add makes the next line: kind, and body (a struct encoding as a JSON object, or nil).
func (w *Writer) Add(kind string, body any, now time.Time) ([]byte, error) {
	if kind == "" || kind == KindDevice {
		return nil, errors.New("not a kind of line to add")
	}
	raw, err := Encode(w.key, Header{Dev: w.last.Dev, Seq: w.last.Seq + 1, Prev: w.last.Hash(),
		Time: now.UTC().Format(timeFormat), Kind: kind}, body)
	if err != nil {
		return nil, err
	}
	l, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	w.last = l
	return raw, nil
}
