// Package usage tracks Gemini token spend from the `stash serve`
// daemon's identify worker. Maintains a JSON file at
// ~/.stash/gemini-usage.json that mirrors the per-model totals
// schema the Mac (UserDefaults) and Android (SharedPreferences)
// already use, so any consumer can read it without a translation
// layer.
//
// Why a file rather than a DB table: matches the pattern we
// already shipped for ~/.stash/gemini-pricing.json (file-on-disk
// served verbatim via gostash HTTP). Mac reads it directly off
// disk; Android reads via `GET /gemini-usage`. New consumers can
// be added without a schema migration — the daemon just appends
// known fields when the model emits them.
//
// Concurrency: the ledger is created once in `stash serve` and
// shared with the identify worker. Record() is safe for
// concurrent use; writes are serialized through a single mutex
// and persisted atomically (write tmp → fsync → rename).
package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ModelBucket matches the Mac/Android shape exactly so a file
// written here decodes cleanly on both sides.
type ModelBucket struct {
	Calls        int   `json:"calls"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// PerModelTotals is a name→bucket map plus convenience views.
// JSON keys match what the Mac/Android sides expect for an
// overlay parse.
type PerModelTotals struct {
	ByModel map[string]ModelBucket `json:"by_model"`
}

// Snapshot is the full ledger contents — today's bucket, the
// all-time bucket, the date today refers to (rolls over when
// the daemon's local date changes), and the first-seen date for
// daily-average / projection math on the client side.
type Snapshot struct {
	Today         PerModelTotals `json:"today"`
	ThisMonth     PerModelTotals `json:"this_month,omitempty"`
	AllTime       PerModelTotals `json:"all_time"`
	Date          string         `json:"date"`
	Month         string         `json:"month,omitempty"` // e.g. "2026-06"
	FirstSeenDate string         `json:"first_seen_date,omitempty"`
}

// Ledger persists Record() calls to disk. Construct with New;
// Record from any goroutine.
type Ledger struct {
	path string
	mu   sync.Mutex
	now  func() time.Time // injectable for tests
}

// New constructs a ledger that writes to ~/.stash/gemini-usage.json.
// The file is created on the first Record() call; until then it
// doesn't exist (so a fresh install doesn't ship a stale zero
// file from a prior run).
func New(stashDir string) *Ledger {
	return &Ledger{
		path: filepath.Join(stashDir, "gemini-usage.json"),
		now:  time.Now,
	}
}

// Path is the absolute path to the ledger file. Exposed so the
// HTTP layer can serve it verbatim (same pattern as the pricing
// file).
func (l *Ledger) Path() string { return l.path }

// Record appends one call's usage. Implements the
// identify.UsageRecorder interface — wired into the daemon's
// worker by `stash serve` startup.
func (l *Ledger) Record(model string, promptTokens, candidatesTokens int) {
	if model == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	snap, _ := l.loadLocked()
	if snap.FirstSeenDate == "" {
		snap.FirstSeenDate = snap.Date
	}
	if snap.Today.ByModel == nil {
		snap.Today.ByModel = map[string]ModelBucket{}
	}
	if snap.ThisMonth.ByModel == nil {
		snap.ThisMonth.ByModel = map[string]ModelBucket{}
	}
	if snap.AllTime.ByModel == nil {
		snap.AllTime.ByModel = map[string]ModelBucket{}
	}

	add := func(t PerModelTotals, model string, in, out int64) PerModelTotals {
		b := t.ByModel[model]
		b.Calls++
		b.InputTokens += in
		b.OutputTokens += out
		t.ByModel[model] = b
		return t
	}
	snap.Today = add(snap.Today, model, int64(promptTokens), int64(candidatesTokens))
	snap.ThisMonth = add(snap.ThisMonth, model, int64(promptTokens), int64(candidatesTokens))
	snap.AllTime = add(snap.AllTime, model, int64(promptTokens), int64(candidatesTokens))

	if err := l.writeLocked(snap); err != nil {
		fmt.Fprintf(os.Stderr, "[usage] write %s: %v\n", l.path, err)
	}
}

// Load returns the current snapshot. Used by the HTTP handler
// and any in-process consumer. Returns an empty snapshot when
// the file doesn't exist yet (fresh install).
func (l *Ledger) Load() (Snapshot, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loadLocked()
}

func (l *Ledger) loadLocked() (Snapshot, error) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{
				Today:     PerModelTotals{ByModel: map[string]ModelBucket{}},
				ThisMonth: PerModelTotals{ByModel: map[string]ModelBucket{}},
				AllTime:   PerModelTotals{ByModel: map[string]ModelBucket{}},
				Date:      l.now().Format("2006-01-02"),
				Month:     l.now().Format("2006-01"),
			}, nil
		}
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("decode %s: %w", l.path, err)
	}

	// Rollover checks during Load to prevent budget lockouts on day/month transition
	today := l.now().Format("2006-01-02")
	currentMonth := l.now().Format("2006-01")
	changed := false

	if snap.Date != today {
		snap.Today = PerModelTotals{ByModel: map[string]ModelBucket{}}
		snap.Date = today
		changed = true
	}
	if snap.Month != currentMonth {
		snap.ThisMonth = PerModelTotals{ByModel: map[string]ModelBucket{}}
		snap.Month = currentMonth
		changed = true
	}
	if snap.FirstSeenDate == "" {
		snap.FirstSeenDate = today
		changed = true
	}

	if changed {
		if err := l.writeLocked(snap); err != nil {
			fmt.Fprintf(os.Stderr, "[usage] rollover write %s: %v\n", l.path, err)
		}
	}

	return snap, nil
}

func (l *Ledger) writeLocked(snap Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), "gemini-usage-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	var closed bool
	var success bool
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(buf); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	closed = true
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, l.path); err != nil {
		return err
	}
	success = true
	return nil
}

// Pricing models and budget-checking logic

type ModelPricing struct {
	InputPerMillion  float64 `json:"input_per_million"`
	OutputPerMillion float64 `json:"output_per_million"`
}

type PricingCatalog struct {
	DefaultModel string                  `json:"default_model"`
	Models       map[string]ModelPricing `json:"models"`
}

const defaultPricingJSON = `{
  "default_model": "gemini-2.5-flash",
  "models": {
    "gemini-2.5-flash":      { "input_per_million": 0.30, "output_per_million": 2.50 },
    "gemini-2.5-flash-lite": { "input_per_million": 0.10, "output_per_million": 0.40 },
    "gemini-2.5-pro":        { "input_per_million": 1.25, "output_per_million": 10.00 },
    "gemini-3-flash":        { "input_per_million": 0.50, "output_per_million": 3.00 },
    "gemini-3-pro":          { "input_per_million": 2.00, "output_per_million": 12.00 },
    "gemini-3.1-flash":      { "input_per_million": 0.30, "output_per_million": 2.50 },
    "gemini-3.1-pro":        { "input_per_million": 1.25, "output_per_million": 10.00 }
  }
}
`

func (l *Ledger) loadPricing() (PricingCatalog, error) {
	pricingPath := filepath.Join(filepath.Dir(l.path), "gemini-pricing.json")
	data, err := os.ReadFile(pricingPath)
	var catalog PricingCatalog
	if err == nil {
		if err := json.Unmarshal(data, &catalog); err == nil {
			return catalog, nil
		}
	}
	// Fallback to default pricing JSON
	if err := json.Unmarshal([]byte(defaultPricingJSON), &catalog); err == nil {
		return catalog, nil
	}
	return PricingCatalog{}, fmt.Errorf("failed to load pricing catalog")
}

func (c PricingCatalog) GetPricingForModel(modelName string) ModelPricing {
	modelName = strings.TrimPrefix(modelName, "models/")
	if p, ok := c.Models[modelName]; ok {
		return p
	}
	if p, ok := c.Models[c.DefaultModel]; ok {
		return p
	}
	return ModelPricing{
		InputPerMillion:  0.30,
		OutputPerMillion: 2.50,
	}
}

func (c PricingCatalog) CalculateCost(totals PerModelTotals) float64 {
	var totalCost float64
	for modelName, bucket := range totals.ByModel {
		pricing := c.GetPricingForModel(modelName)
		inputCost := (float64(bucket.InputTokens) * pricing.InputPerMillion) / 1000000.0
		outputCost := (float64(bucket.OutputTokens) * pricing.OutputPerMillion) / 1000000.0
		totalCost += inputCost + outputCost
	}
	return totalCost
}

func (l *Ledger) CheckBudget(maxDailyUSD, maxMonthlyUSD float64) (bool, bool, error) {
	snap, err := l.Load()
	if err != nil {
		return false, false, err
	}
	catalog, err := l.loadPricing()
	if err != nil {
		return false, false, err
	}

	dailyCost := catalog.CalculateCost(snap.Today)
	monthlyCost := catalog.CalculateCost(snap.ThisMonth)

	dailyExceeded := maxDailyUSD > 0 && dailyCost >= maxDailyUSD
	monthlyExceeded := maxMonthlyUSD > 0 && monthlyCost >= maxMonthlyUSD

	return dailyExceeded, monthlyExceeded, nil
}

func (l *Ledger) IsBudgetExceeded(maxDailyUSD, maxMonthlyUSD float64) (bool, error) {
	dailyExceeded, monthlyExceeded, err := l.CheckBudget(maxDailyUSD, maxMonthlyUSD)
	if err != nil {
		return false, err
	}
	return dailyExceeded || monthlyExceeded, nil
}
