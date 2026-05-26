package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLedgerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	// Fix the clock so date rollover is deterministic.
	l.now = func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }

	l.Record("gemini-2.5-flash", 100, 50)
	l.Record("gemini-2.5-flash", 200, 75)
	l.Record("gemini-2.5-pro", 500, 1000)

	snap, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Date != "2026-05-20" {
		t.Errorf("Date = %q, want 2026-05-20", snap.Date)
	}
	if snap.FirstSeenDate != "2026-05-20" {
		t.Errorf("FirstSeenDate = %q, want 2026-05-20", snap.FirstSeenDate)
	}
	flash := snap.Today.ByModel["gemini-2.5-flash"]
	if flash.Calls != 2 || flash.InputTokens != 300 || flash.OutputTokens != 125 {
		t.Errorf("Today flash bucket = %+v, want {2,300,125}", flash)
	}
	pro := snap.Today.ByModel["gemini-2.5-pro"]
	if pro.Calls != 1 || pro.InputTokens != 500 || pro.OutputTokens != 1000 {
		t.Errorf("Today pro bucket = %+v, want {1,500,1000}", pro)
	}
	// All-time should equal today on the first day.
	if snap.AllTime.ByModel["gemini-2.5-flash"] != flash {
		t.Errorf("AllTime flash != Today flash on first day")
	}

	// Verify the on-disk file matches the snapshot (the HTTP
	// endpoint will serve the file verbatim).
	raw, err := os.ReadFile(filepath.Join(dir, "gemini-usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Today.ByModel["gemini-2.5-flash"] != flash {
		t.Errorf("file decode mismatch")
	}
}

func TestLedgerDateRollover(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	day1 := time.Date(2026, 5, 20, 23, 59, 0, 0, time.UTC)
	day2 := time.Date(2026, 5, 21, 0, 1, 0, 0, time.UTC)

	l.now = func() time.Time { return day1 }
	l.Record("gemini-2.5-flash", 100, 50)

	l.now = func() time.Time { return day2 }
	l.Record("gemini-2.5-flash", 200, 100)

	snap, _ := l.Load()
	if snap.Date != "2026-05-21" {
		t.Errorf("Date = %q after rollover, want 2026-05-21", snap.Date)
	}
	if snap.FirstSeenDate != "2026-05-20" {
		t.Errorf("FirstSeenDate = %q, want 2026-05-20 (unchanged)", snap.FirstSeenDate)
	}
	today := snap.Today.ByModel["gemini-2.5-flash"]
	if today.Calls != 1 || today.InputTokens != 200 || today.OutputTokens != 100 {
		t.Errorf("Today after rollover = %+v, want {1,200,100} (only day2 call)", today)
	}
	allTime := snap.AllTime.ByModel["gemini-2.5-flash"]
	if allTime.Calls != 2 || allTime.InputTokens != 300 || allTime.OutputTokens != 150 {
		t.Errorf("AllTime after rollover = %+v, want {2,300,150}", allTime)
	}
}

func TestLedgerEmptyOnFreshInstall(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	l.now = func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }
	snap, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Today.ByModel) != 0 || len(snap.AllTime.ByModel) != 0 {
		t.Errorf("fresh ledger should have empty buckets, got %+v", snap)
	}
	// Should not have written a file just because Load() was called.
	if _, err := os.Stat(filepath.Join(dir, "gemini-usage.json")); !os.IsNotExist(err) {
		t.Errorf("Load() created file unexpectedly")
	}
}
