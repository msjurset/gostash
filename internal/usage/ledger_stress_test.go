package usage

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLedgerRolloverAndLockContention verifies that date/month rollover behaves correctly
// during concurrent Load() and Record() operations, and that budget overflow does not cause
// a permanent lockout.
func TestLedgerRolloverAndLockContention(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)

	// We use an atomic counter to safely advance time across concurrent goroutines
	var timeSecs int64 = time.Date(2026, 6, 21, 23, 59, 50, 0, time.UTC).Unix()
	l.now = func() time.Time {
		return time.Unix(atomic.LoadInt64(&timeSecs), 0).UTC()
	}

	// 1. Stress test concurrent Load() and Record() with date/month rollovers.
	var wg sync.WaitGroup
	numGoroutines := 100
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			// Alternating calls of Record and Load
			if id%2 == 0 {
				l.Record("gemini-2.5-flash", 100, 50)
			} else {
				_, _ = l.Load()
			}
			// Concurrently advance time to trigger rollovers
			atomic.AddInt64(&timeSecs, 1) // adding seconds
		}(i)
	}

	wg.Wait()

	// 2. Check that exceeding the budget does not cause a permanent lockout.
	// Reset time back to day 1
	day1 := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return day1 }

	// Record enough tokens to exceed daily budget of 0.01 USD
	// Daily limit of 0.01 USD.
	// gemini-2.5-pro: input = 1.25 per million, output = 10.00 per million
	// Let's record a huge usage: 10,000,000 input tokens -> cost is 12.50 USD
	l.Record("gemini-2.5-pro", 10000000, 0)

	exceeded, err := l.IsBudgetExceeded(0.01, 100.0)
	if err != nil {
		t.Fatal(err)
	}
	if !exceeded {
		t.Error("expected daily budget to be exceeded")
	}

	// Now simulate date rollover to day 2 (next day)
	day2 := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return day2 }

	// Verify that budget is no longer exceeded because of day rollover
	exceeded, err = l.IsBudgetExceeded(0.01, 100.0)
	if err != nil {
		t.Fatal(err)
	}
	if exceeded {
		t.Error("expected daily budget to be cleared after date rollover (no permanent lockout)")
	}
}

// TestLedgerResourceCleanups verifies that no temp files are leaked on write errors.
func TestLedgerResourceCleanups(t *testing.T) {
	dir := t.TempDir()
	
	// Create a subdirectory that we will make unwriteable
	subDir := filepath.Join(dir, "unwriteable")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	l := New(subDir)
	l.now = func() time.Time { return time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC) }

	// Record a valid call first to verify it works
	l.Record("gemini-2.5-flash", 10, 5)

	// Now make the subDir unwriteable
	if err := os.Chmod(subDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(subDir, 0755)
	})

	// This should fail to write
	l.Record("gemini-2.5-flash", 100, 50)

	// Restore permission so we can read the dir
	if err := os.Chmod(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Verify that no .tmp files exist in subDir
	files, err := os.ReadDir(subDir)
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range files {
		name := file.Name()
		if strings.HasSuffix(name, ".tmp") {
			t.Errorf("found leaked temp file: %s", name)
		}
	}
}
