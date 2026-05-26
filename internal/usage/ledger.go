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
	AllTime       PerModelTotals `json:"all_time"`
	Date          string         `json:"date"`
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
	today := l.now().Format("2006-01-02")

	// Date rollover — if the snapshot's date is stale, reset
	// today's bucket but keep all-time.
	if snap.Date != today {
		snap.Today = PerModelTotals{ByModel: map[string]ModelBucket{}}
		snap.Date = today
	}
	if snap.FirstSeenDate == "" {
		snap.FirstSeenDate = today
	}
	if snap.Today.ByModel == nil {
		snap.Today.ByModel = map[string]ModelBucket{}
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
	snap.AllTime = add(snap.AllTime, model, int64(promptTokens), int64(candidatesTokens))

	if err := l.writeLocked(snap); err != nil {
		// Best-effort — log but don't fail the identify path.
		// Caller's worker handles the actual identify outcome;
		// dropped usage is a visibility loss, not a correctness
		// loss.
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
				Today:   PerModelTotals{ByModel: map[string]ModelBucket{}},
				AllTime: PerModelTotals{ByModel: map[string]ModelBucket{}},
				Date:    l.now().Format("2006-01-02"),
			}, nil
		}
		return Snapshot{}, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("decode %s: %w", l.path, err)
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
	cleanup := func() { os.Remove(tmpName) }
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	return os.Rename(tmpName, l.path)
}
