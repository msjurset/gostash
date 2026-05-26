// Package identify provides the background worker that the
// `stash serve` daemon runs to auto-identify items tagged
// `needs-identify`. Sortie tags new ingest items from configured
// sites (Google Photos, etc.); the worker reads the tag as a
// queue, calls Gemini, writes title/notes/transcript back, and
// drops the tag on success.
//
// Defensive behavior baked in (no separate hardening pass):
//
//   - Empty Keychain (no key cached): worker logs once, idles
//     by skipping ticks until a key appears. Re-running
//     `stash auth set-gemini` wakes the worker on its next tick.
//
//   - Stale key (Gemini 401/403): worker pins the rejected key,
//     stops calling Gemini, logs a clear "run stash auth
//     refresh-gemini" message. When the key value in Keychain
//     differs from the pinned-rejected value, the worker resumes
//     automatically — refresh-gemini is the only manual step.
//
//   - Transient errors (5xx, plain 429, network): tag stays,
//     the same item is picked up on the next poll. No attempt
//     counter incremented.
//
//   - Permanent errors (parse failures, missing file, quota
//     exhausted): per-item attempt counter increments. After
//     MaxAttempts, the worker stops re-trying THIS item for the
//     rest of the daemon's lifetime. Counter resets on daemon
//     restart so a config / key fix lets the item flow again.
//
//   - In-flight Gemini calls use a context derived from
//     context.Background, NOT the worker context. SIGTERM cancels
//     the polling loop but lets the in-flight call finish so a
//     paid request doesn't get abandoned mid-response. The serve
//     command's WaitGroup waits for the worker to exit before
//     the daemon shuts down.
package identify

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/msjurset/gostash/internal/credentials"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/gemini"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"
)

// Tag name applied by sortie / by anything else that wants the
// daemon to pick an item up for AI identify. Centralized so
// every producer (sortie rule, future Android, etc.) agrees on
// one spelling.
const Tag = "needs-identify"

// UsageRecorder is the hook the worker uses to report per-call
// token usage. Step 5's ledger plugs in here. Default is a
// no-op so the worker is functional before the ledger exists.
type UsageRecorder interface {
	Record(model string, promptTokens, candidatesTokens int)
}

type noopRecorder struct{}

func (noopRecorder) Record(string, int, int) {}

// Options control the worker's polling cadence and per-item
// retry budget. Zero values fall back to sane defaults.
type Options struct {
	// PollInterval is how often the worker queries for pending
	// items. Default 30s. Cheap enough — a single SQL query
	// against an indexed tag join.
	PollInterval time.Duration
	// BatchSize caps how many items the worker pulls per tick.
	// Limits the worst-case time-to-drain when a large Photos
	// export lands all at once. Default 10.
	BatchSize int
	// MaxAttempts is how many times a single item can fail with
	// a non-transient error before the worker stops retrying it
	// (for this daemon lifetime). Default 5.
	MaxAttempts int
	// CallTimeout caps a single Gemini call. Default 60s, fits
	// inside the plist's ExitTimeOut=60s budget — see serve.go's
	// graceful-shutdown timing comment.
	CallTimeout time.Duration
	// Recorder is plugged in by the daemon when the usage ledger
	// is wired (step 5). Worker is robust to nil.
	Recorder UsageRecorder
	// Logger overrides log.Default(). Useful in tests.
	Logger *log.Logger
}

func (o *Options) applyDefaults() {
	if o.PollInterval <= 0 {
		o.PollInterval = 30 * time.Second
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 10
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = 60 * time.Second
	}
	if o.Recorder == nil {
		o.Recorder = noopRecorder{}
	}
	if o.Logger == nil {
		o.Logger = log.Default()
	}
}

// Worker is the polling identify worker. Construct via New;
// drive with Run.
type Worker struct {
	store store.Store
	fs    *filestore.FileStore
	gem   *gemini.Client
	opts  Options

	mu             sync.Mutex
	attempts       map[string]int
	lastRejectedKey string
	lastKeyLogged  bool
}

// New constructs a worker. Caller owns the lifetimes of store /
// fs / gem — the worker just holds references.
func New(s store.Store, fs *filestore.FileStore, gem *gemini.Client, opts Options) *Worker {
	opts.applyDefaults()
	return &Worker{
		store:    s,
		fs:       fs,
		gem:      gem,
		opts:     opts,
		attempts: make(map[string]int),
	}
}

// Run blocks the calling goroutine until ctx is cancelled,
// polling on the configured interval. Safe to use as the worker
// goroutine body in `serve.go`'s WaitGroup pattern:
//
//	workers.Add(1)
//	go func() { defer workers.Done(); w.Run(ctx) }()
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.opts.PollInterval)
	defer ticker.Stop()

	// First tick fires immediately so a freshly-started daemon
	// picks up the queue without waiting PollInterval.
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	key, err := credentials.Load(credentials.KeyGeminiAPIKey)
	if err != nil {
		w.opts.Logger.Printf("[identify] keychain load error: %v", err)
		return
	}
	if key == "" {
		if w.lastKeyLogged {
			w.opts.Logger.Printf("[identify] no Gemini key cached — idling until `stash auth set-gemini` runs")
			w.lastKeyLogged = false
		}
		return
	}
	if !w.lastKeyLogged {
		w.opts.Logger.Printf("[identify] Gemini key available; worker active")
		w.lastKeyLogged = true
	}
	// Same value Gemini already rejected on a prior tick?
	// Don't burn the call.
	w.mu.Lock()
	rejected := w.lastRejectedKey == key && key != ""
	w.mu.Unlock()
	if rejected {
		return
	}

	items, err := w.store.ListItems(ctx, model.ItemFilter{
		Tags:  []string{Tag},
		Limit: w.opts.BatchSize,
		Type:  model.TypeImage,
	})
	if err != nil {
		w.opts.Logger.Printf("[identify] list pending items: %v", err)
		return
	}
	for i := range items {
		if ctx.Err() != nil {
			return
		}
		item := items[i]
		if w.shouldSkip(item.ID) {
			continue
		}
		if perr := w.processOne(ctx, &item, key); perr != nil {
			if gemini.IsKeyRejected(perr) {
				w.mu.Lock()
				w.lastRejectedKey = key
				w.mu.Unlock()
				w.opts.Logger.Printf("[identify] Gemini rejected the cached key (%v). " +
					"Worker paused — run `stash auth refresh-gemini` to recover.", perr)
				return // stop the rest of this tick's batch
			}
			if gemini.IsTransient(perr) {
				w.opts.Logger.Printf("[identify] transient error on item %s: %v (will retry)", item.ID, perr)
				continue
			}
			n := w.bumpAttempts(item.ID)
			w.opts.Logger.Printf("[identify] permanent error on item %s (attempt %d/%d): %v",
				item.ID, n, w.opts.MaxAttempts, perr)
			continue
		}
		w.clearAttempts(item.ID)
		w.opts.Logger.Printf("[identify] identified item %s", item.ID)
	}
}

func (w *Worker) processOne(ctx context.Context, item *model.Item, key string) error {
	images, err := w.collectImages(item)
	if err != nil {
		return err
	}
	if len(images) == 0 {
		return errors.New("identify: no readable images for item")
	}

	// In-flight Gemini call gets its own ctx — independent of
	// the worker / shutdown context. SIGTERM during this call
	// lets it finish rather than abandoning a paid request.
	callCtx, cancel := context.WithTimeout(context.Background(), w.opts.CallTimeout)
	defer cancel()

	result, err := w.gem.Identify(callCtx, key, images, gemini.DefaultIdentifyPrompt)

	// Record usage even on parse-empty responses or errors so cost
	// tracking reflects all paid calls (like safety filter blocks).
	if result.Model != "" {
		w.opts.Recorder.Record(result.Model, result.PromptTokens, result.CandidatesTokens)
	}

	if err != nil {
		return err
	}

	// Apply identify result conservatively: never overwrite
	// non-empty user content. Title is the exception — items
	// tagged needs-identify by sortie come with auto-generated
	// filenames-as-titles, so replacing those is the desired
	// behavior. If the user has already edited title or notes
	// before the worker got to them, leave them alone.
	changed := false
	if result.Title != "" && shouldReplaceTitle(item.Title) {
		item.Title = result.Title
		changed = true
	}
	if result.Notes != "" && item.Notes == "" {
		item.Notes = result.Notes
		changed = true
	}
	if result.Transcript != "" && item.ExtractedText == "" {
		item.ExtractedText = result.Transcript
		changed = true
	}
	if changed {
		if err := w.store.UpdateItem(ctx, item); err != nil {
			return err
		}
	}
	if err := w.store.RemoveTag(ctx, item.ID, Tag); err != nil {
		return err
	}
	return nil
}

// shouldReplaceTitle returns true when the current title looks
// auto-generated (filename-ish) and replacing it with the
// Gemini-identified name improves things. Heuristic: stash add
// sets the title to the file's basename when no explicit title
// is supplied, so most needs-identify items arrive with titles
// like `IMG_20240515_123456.jpg` or `Photos-2024.zip-image-3.jpg`.
// Anything looking like that gets overwritten; richer titles
// (already-edited by the user) are preserved.
func shouldReplaceTitle(current string) bool {
	trimmed := strings.TrimSpace(current)
	if trimmed == "" {
		return true
	}
	// Common auto-title shapes by case-insensitive prefix. Camera
	// roll formats (IMG_, DSC, PXL_), screenshot tools, the
	// Photos zip extraction naming (Photos-…), and our own
	// reroute subdir name (stash-) all signal "this came from
	// a machine, replace freely."
	prefixes := []string{"img_", "dsc", "pxl_", "photos-", "photos ", "screenshot", "stash-"}
	low := strings.ToLower(trimmed)
	for _, p := range prefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	// Ends in an image / video extension (case-insensitive) →
	// almost always a filename masquerading as a title.
	exts := []string{".jpg", ".jpeg", ".png", ".heic", ".gif", ".webp", ".tiff", ".bmp", ".mp4", ".mov", ".m4v"}
	for _, e := range exts {
		if strings.HasSuffix(low, e) {
			return true
		}
	}
	return false
}

func (w *Worker) collectImages(item *model.Item) ([]gemini.Image, error) {
	var images []gemini.Image
	if item.ContentHash != "" {
		data, err := readBlob(w.fs, item.ContentHash)
		if err != nil {
			return nil, err
		}
		images = append(images, gemini.Image{
			Data:     data,
			MimeType: mimeOr(item.MimeType, "image/jpeg"),
		})
	}
	for _, f := range item.Files {
		if f.ContentHash == "" {
			continue
		}
		data, err := readBlob(w.fs, f.ContentHash)
		if err != nil {
			// One bad attached file shouldn't tank the
			// whole identify — log and continue with what
			// we have.
			w.opts.Logger.Printf("[identify] skipping attached file %d on item %s: %v", f.ID, item.ID, err)
			continue
		}
		images = append(images, gemini.Image{
			Data:     data,
			MimeType: mimeOr(f.MimeType, "image/jpeg"),
		})
	}
	return images, nil
}

func readBlob(fs *filestore.FileStore, hash string) ([]byte, error) {
	rc, err := fs.Open(hash)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func mimeOr(m, fallback string) string {
	if m != "" {
		return m
	}
	return fallback
}

func (w *Worker) shouldSkip(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.attempts[id] >= w.opts.MaxAttempts
}

func (w *Worker) bumpAttempts(id string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.attempts[id]++
	return w.attempts[id]
}

func (w *Worker) clearAttempts(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.attempts, id)
}
