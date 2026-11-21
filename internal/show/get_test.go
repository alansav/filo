package show

import (
	"testing"
	"time"

	"github.com/alansav/filo/internal/store"
)

func TestValue(t *testing.T) {
	mtime := time.Date(2022, 2, 3, 9, 14, 0, 0, time.Local)
	f := File{Shown: store.Shown{FileItem: store.FileItem{RootPath: "/r", Path: "a.pdf", Kind: "file", Size: 188416,
		MtimeNS: mtime.UnixNano(), Hash: "3f9a", FileDate: "2022-01-31", Retention: "bills", Policy: "bills",
		PolicyValue: "7y", KeepUntil: "2029-01-31", Status: store.StatusKept}, Present: true,
		FirstSeenUTC: time.Date(2026, 11, 12, 12, 0, 0, 0, time.Local).UTC().Format(time.RFC3339)}}
	want := map[string]string{"file-date": "2022-01-31", "retention": "bills", "keep-until": "2029-01-31",
		"status": "kept", "duplicates": "counted", "size": "188416", "modified": mtime.Format(time.RFC3339),
		"sha256": "3f9a", "root": "/r", "first-seen": "2026-11-12"}
	for _, fl := range Fields {
		if got := f.Value(fl.Flag); got != want[fl.Flag] {
			t.Errorf("%s = %q, want %q", fl.Flag, got, want[fl.Flag])
		}
	}
	f.Always = true
	if f.Value("duplicates") != "always" {
		t.Errorf("always: %q", f.Value("duplicates"))
	}
	f.Always, f.RecordSet = false, 3
	if f.Value("duplicates") != "records" {
		t.Errorf("records: %q", f.Value("duplicates"))
	}
	f.Present = false
	if f.Value("status") != "missing" {
		t.Errorf("missing: %q", f.Value("status"))
	}
	f.DeletedUTC = "2026-11-16T10:00:00Z"
	if f.Value("status") != "deleted" {
		t.Errorf("deleted: %q", f.Value("status"))
	}
	f.FileDate, f.Kind = "", "symlink"
	if f.Value("file-date") != "" || f.Value("duplicates") != "" {
		t.Errorf("empty values: %q %q", f.Value("file-date"), f.Value("duplicates"))
	}
}
