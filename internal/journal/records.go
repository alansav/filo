package journal

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/alansav/filo/internal/device"
)

// The mark and the status files are one signed line each, in the journal's form (seq 1, no prev): the device that
// wrote it is the header's dev, and its signature is checked with that device's key, from its journal's line 1.

// Kinds of the mark and the status files.
const (
	KindMark   = "mark"
	KindStatus = "status"
)

// MarkBody is the mark's: the synced directory's random ID (in a device ID's form), and when it was marked.
type MarkBody struct {
	ID     string `json:"id"`
	Marked string `json:"marked"`
}

// StatusBody is a status file's: when its device last ran on the synced directory, and the highest line it has
// applied from each journal (by device).
type StatusBody struct {
	Seen    string           `json:"seen"`
	Applied map[string]int64 `json:"applied"`
}

// NewMark makes the mark of a synced directory with the ID id, by the device dev.
func NewMark(key ed25519.PrivateKey, dev, id string, now time.Time) ([]byte, error) {
	if !device.ValidID(dev) {
		return nil, errors.New("not a device ID")
	}
	if !device.ValidID(id) {
		return nil, errors.New("not a synced directory's ID")
	}
	at := now.UTC().Format(timeFormat)
	return Encode(key, Header{Dev: dev, Seq: 1, Time: at, Kind: KindMark}, MarkBody{ID: id, Marked: at})
}

// NewStatus makes the status file of device dev.
func NewStatus(key ed25519.PrivateKey, dev string, applied map[string]int64, now time.Time) ([]byte, error) {
	if !device.ValidID(dev) {
		return nil, errors.New("not a device ID")
	}
	for d, seq := range applied {
		if !device.ValidID(d) || seq < 0 {
			return nil, fmt.Errorf("a status naming %q at line %d", d, seq)
		}
	}
	at := now.UTC().Format(timeFormat)
	if applied == nil {
		applied = map[string]int64{}
	}
	return Encode(key, Header{Dev: dev, Seq: 1, Time: at, Kind: KindStatus}, StatusBody{Seen: at, Applied: applied})
}

// ReadRecord reads the mark or a status file (data, the whole file) of the kind wanted: one complete line, of that
// kind. Its signature is not checked: that needs its device's key (Line.Verify).
func ReadRecord(data []byte, kind string) (Line, error) {
	raw, rest, ok := nextLine(data)
	if !ok || len(bytes.TrimSpace(rest)) > 0 {
		return Line{}, fmt.Errorf("not one line")
	}
	l, err := Parse(raw)
	if err != nil {
		return Line{}, err
	}
	if l.Kind != kind || l.Seq != 1 {
		return Line{}, fmt.Errorf("not a %s", kind)
	}
	return l, nil
}

// Mark reads the mark's body from its line, checking the ID.
func (l Line) Mark() (MarkBody, error) {
	var b MarkBody
	if err := l.Body(&b); err != nil {
		return MarkBody{}, err
	}
	if !device.ValidID(b.ID) {
		return MarkBody{}, errors.New("the mark's ID is not valid")
	}
	return b, nil
}

// Status reads a status file's body from its line, dropping any entry that does not name a device.
func (l Line) Status() (StatusBody, error) {
	var b StatusBody
	if err := l.Body(&b); err != nil {
		return StatusBody{}, err
	}
	for dev, seq := range b.Applied {
		if !device.ValidID(dev) || seq < 0 {
			delete(b.Applied, dev)
		}
	}
	return b, nil
}
