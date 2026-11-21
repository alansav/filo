package show

import (
	"strconv"
	"time"

	"github.com/alansav/filo/internal/store"
)

// The fields filo get prints (docs/design.md, Showing and setting a file's details): set's, and what Filo observed.
// Each is named as its flag, without the dashes, and printed under its register name.
var Fields = []struct{ Flag, Name string }{
	{"file-date", "file_date"},
	{"retention", "retention"},
	{"keep-until", "keep_until"},
	{"status", "status"},
	{"duplicates", "duplicates"},
	{"size", "size"},
	{"modified", "modified"},
	{"sha256", "sha256"},
	{"root", "root"},
	{"first-seen", "first_seen"},
}

// Value is a field of the file for a script: as stored where it is one (a date YYYY-MM-DD, a retention as typed or a
// policy's name), "" for none. status is the status's register name (kept, undated, needs_file_date …), or missing or
// deleted for a file no longer present; duplicates is the content's decision (always, records or counted); size is in
// bytes; modified is RFC 3339 in local time; first_seen is the local date of the scan that first saw it.
func (f File) Value(flag string) string {
	switch flag {
	case "file-date":
		return f.FileDate
	case "retention":
		return f.Retention
	case "keep-until":
		return f.KeepUntil
	case "status":
		switch {
		case f.DeletedUTC != "":
			return "deleted"
		case !f.Present:
			return "missing"
		}
		return string(f.Status)
	case "duplicates":
		switch {
		case f.Kind != "file":
			return ""
		case f.Always:
			return store.DupAlways
		case f.RecordSet != 0:
			return store.DupRecords
		}
		return store.DupCounted
	case "size":
		return itoa(f.Size)
	case "modified":
		return time.Unix(0, f.MtimeNS).Local().Format(time.RFC3339)
	case "sha256":
		return f.Hash
	case "root":
		return f.RootPath
	case "first-seen":
		return date(f.FirstSeenUTC)
	}
	return ""
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
