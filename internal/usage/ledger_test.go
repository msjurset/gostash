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

func TestLedgerMonthlyRolloverAndBudgets(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)

	// Day 1: May 20, 2026
	day1 := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return day1 }
	l.Record("gemini-2.5-flash", 100000, 50000) // Input = 100k, Output = 50k

	snap, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Month != "2026-05" {
		t.Errorf("Month = %q, want 2026-05", snap.Month)
	}
	
	flash1 := snap.ThisMonth.ByModel["gemini-2.5-flash"]
	if flash1.Calls != 1 || flash1.InputTokens != 100000 || flash1.OutputTokens != 50000 {
		t.Errorf("ThisMonth flash bucket day 1 = %+v, want {1, 100000, 50000}", flash1)
	}

	// Day 2: June 01, 2026 (Month rollover)
	day2 := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return day2 }
	l.Record("gemini-2.5-flash", 20000, 10000) // Input = 20k, Output = 10k

	snap, err = l.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Month != "2026-06" {
		t.Errorf("Month = %q after rollover, want 2026-06", snap.Month)
	}

	flash2 := snap.ThisMonth.ByModel["gemini-2.5-flash"]
	if flash2.Calls != 1 || flash2.InputTokens != 20000 || flash2.OutputTokens != 10000 {
		t.Errorf("ThisMonth flash bucket day 2 = %+v, want {1, 20000, 10000}", flash2)
	}

	allTime := snap.AllTime.ByModel["gemini-2.5-flash"]
	if allTime.Calls != 2 || allTime.InputTokens != 120000 || allTime.OutputTokens != 60000 {
		t.Errorf("AllTime = %+v, want {2, 120000, 60000}", allTime)
	}

	// Test Budget Calculations using default Pricing Catalog.
	// defaultPricingJSON rates for gemini-2.5-flash:
	// input: 0.30 per million -> 0.0000003 per token
	// output: 2.50 per million -> 0.0000025 per token
	// Today's total (day 2): Input 20k, Output 10k.
	// todayCost = (20000 * 0.30) / 1e6 + (10000 * 2.50) / 1e6
	//           = 0.006 + 0.025 = 0.031 USD
	// This Month's total: Input 20k, Output 10k.
	// monthCost = 0.031 USD

	// If we set maxDaily = 0.030, it should exceed daily
	dailyExceeded, monthlyExceeded, err := l.CheckBudget(0.030, 0.050)
	if err != nil {
		t.Fatal(err)
	}
	if !dailyExceeded {
		t.Errorf("Expected daily budget to be exceeded (0.031 >= 0.030)")
	}
	if monthlyExceeded {
		t.Errorf("Did not expect monthly budget to be exceeded (0.031 < 0.050)")
	}

	// If we set maxMonthly = 0.030, it should exceed monthly
	dailyExceeded, monthlyExceeded, err = l.CheckBudget(0.050, 0.030)
	if err != nil {
		t.Fatal(err)
	}
	if dailyExceeded {
		t.Errorf("Did not expect daily budget to be exceeded (0.031 < 0.050)")
	}
	if !monthlyExceeded {
		t.Errorf("Expected monthly budget to be exceeded (0.031 >= 0.030)")
	}

	// Test IsBudgetExceeded
	exceeded, err := l.IsBudgetExceeded(0.050, 0.030)
	if err != nil {
		t.Fatal(err)
	}
	if !exceeded {
		t.Errorf("Expected IsBudgetExceeded to return true")
	}

	// Write custom pricing file gemini-pricing.json
	customPricing := `{
		"default_model": "gemini-2.5-flash",
		"models": {
			"gemini-2.5-flash": { "input_per_million": 10.0, "output_per_million": 20.0 }
		}
	}`
	err = os.WriteFile(filepath.Join(dir, "gemini-pricing.json"), []byte(customPricing), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Custom price: Input 20k @ 10.0/M = 0.20 USD. Output 10k @ 20.0/M = 0.20 USD. Total = 0.40 USD.
	// Daily budget 0.35 USD should be exceeded now.
	dailyExceeded, monthlyExceeded, err = l.CheckBudget(0.35, 1.00)
	if err != nil {
		t.Fatal(err)
	}
	if !dailyExceeded {
		t.Errorf("Expected daily budget to be exceeded under custom pricing (0.40 >= 0.35)")
	}
}

