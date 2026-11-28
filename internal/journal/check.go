package journal

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/alansav/filo/internal/device"
)

// Point is where reading a journal stopped: the byte offset just after the last line accepted (its newline), that
// line itself, and the device's key from line 1. The zero Point (no line accepted yet) is read on from by Check,
// from the start. Reading on from a Point checks first that the line before it is
// still there, byte for byte, so a journal changed under it is read and checked again from the start.
type Point struct {
	Offset int64
	Last   Line
	Key    ed25519.PublicKey
}

// Result is what checking a journal found.
type Result struct {
	Device DeviceBody        // line 1's body (when read from the start)
	Key    ed25519.PublicKey // the device's key, from line 1
	Lines  []Line            // the lines accepted, in order
	End    Point             // where to read on from next time
	// Partial: the journal ends in a line not yet complete (no newline): not read, and not a problem; a sync
	// service may be part way through writing it.
	Partial bool
	// Stop is why reading stopped before the end (nil: it did not): a line that is not one, a bad signature, a
	// broken chain, or a newer format (errors.Is(Stop, ErrNewer)). Nothing after it is accepted.
	Stop error
}

// ErrChanged is a journal that is no longer what was read from it before: the line before the point to read on
// from is not there as it was.
var ErrChanged = errors.New("the journal changed since it was last read")

// Check reads a whole journal: data is the file, dev the device its name says it is. Line 1 must be that device
// introducing itself; every line must be signed with line 1's key, name that device, and follow the line before it
// (seq one more, prev its hash).
func Check(data []byte, dev string) Result {
	var res Result
	raw, rest, ok := nextLine(data)
	if !ok {
		res.Partial = len(data) > 0
		return res
	}
	first, err := Parse(raw)
	if err != nil {
		res.Stop = fmt.Errorf("line 1: %w", err)
		return res
	}
	if first.Dev != dev || first.Seq != 1 || first.Kind != KindDevice {
		res.Stop = errors.New("line 1 is not the device introducing itself")
		return res
	}
	var body DeviceBody
	if err := first.Body(&body); err != nil {
		res.Stop = fmt.Errorf("line 1: %w", err)
		return res
	}
	key, err := device.DecodePublic(body.Key)
	if err != nil {
		res.Stop = fmt.Errorf("line 1: %w", err)
		return res
	}
	if device.CheckName(body.Name) != nil {
		res.Stop = errors.New("line 1: the device's name is not valid")
		return res
	}
	if !first.Verify(key) {
		res.Stop = errors.New("line 1: the signature is not the device's")
		return res
	}
	res.Device, res.Key = body, key
	res.Lines = []Line{first}
	res.End = Point{Offset: int64(len(raw) + 1), Last: first, Key: key}
	return checkRest(res, rest)
}

// CheckFrom reads on from p: tail is the journal's bytes from p.Offset - len(p.Last.Raw) - 1 (the last line
// accepted, its newline, and anything after). A tail that does not start with that line is ErrChanged: the caller
// then reads the journal again from the start.
func CheckFrom(tail []byte, p Point) (Result, error) {
	if len(p.Last.Raw) == 0 {
		return Result{}, errors.New("no line read yet to read on from: read the journal from the start")
	}
	want := append(append([]byte(nil), p.Last.Raw...), '\n')
	if !bytes.HasPrefix(tail, want) {
		return Result{}, ErrChanged
	}
	return checkRest(Result{Key: p.Key, End: p}, tail[len(want):]), nil
}

// checkRest accepts the lines in data after res.End, while each is signed and follows the one before.
func checkRest(res Result, data []byte) Result {
	prev := res.End.Last
	for len(data) > 0 {
		raw, rest, ok := nextLine(data)
		if !ok {
			res.Partial = true
			break
		}
		l, err := Parse(raw)
		if err != nil {
			res.Stop = fmt.Errorf("line %d: %w", prev.Seq+1, err)
			break
		}
		switch {
		case l.Dev != prev.Dev:
			res.Stop = fmt.Errorf("line %d names another device", prev.Seq+1)
		case l.Seq != prev.Seq+1:
			res.Stop = fmt.Errorf("line %d is numbered %d", prev.Seq+1, l.Seq)
		case l.Prev != prev.Hash():
			res.Stop = fmt.Errorf("line %d does not follow line %d", l.Seq, prev.Seq)
		case l.Kind == KindDevice:
			res.Stop = fmt.Errorf("line %d introduces the device again", l.Seq)
		case !l.Verify(res.Key):
			res.Stop = fmt.Errorf("line %d: the signature is not the device's", l.Seq)
		}
		if res.Stop != nil {
			break
		}
		res.Lines = append(res.Lines, l)
		res.End = Point{Offset: res.End.Offset + int64(len(raw)+1), Last: l, Key: res.Key}
		prev = l
		data = rest
	}
	return res
}

// nextLine splits off data's first line, without its newline; ok is false when there is no newline (a line not
// yet complete). A line too long to be one is returned as it is, for Parse to refuse.
func nextLine(data []byte) (line, rest []byte, ok bool) {
	i := bytes.IndexByte(data, '\n')
	if i < 0 {
		if len(data) > MaxLine+1 {
			return data[:MaxLine+1], nil, true // never a line: refused, not waited for
		}
		return nil, data, false
	}
	return data[:i], data[i+1:], true
}
