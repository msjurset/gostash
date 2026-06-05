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
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		o.CallTimeout = 30 * time.Second
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
		// Filter by type manually since ListItems with TypeImage was narrowing too much.
		// We want images and videos.
		if item.Type != model.TypeImage && !strings.HasPrefix(item.MimeType, "video/") {
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
			
			// Both transient and permanent errors consume an attempt.
			// This prevents a model-level 503 loop from burning infinitely.
			n := w.bumpAttempts(item.ID)
			
			if gemini.IsTransient(perr) {
				w.opts.Logger.Printf("[identify] transient error on item %s (attempt %d/%d): %v (will retry)", 
					item.ID, n, w.opts.MaxAttempts, perr)
			} else {
				w.opts.Logger.Printf("[identify] permanent error on item %s (attempt %d/%d): %v",
					item.ID, n, w.opts.MaxAttempts, perr)
			}
			
			if n >= w.opts.MaxAttempts {
				w.opts.Logger.Printf("[identify] item %s exhausted retries. Untagging.", item.ID)
				w.clearAttempts(item.ID)
				
				// Remove `needs-identify` and add `identify-failed` to stop the loop
				var newTags []model.Tag
				for _, t := range item.Tags {
					if t.Name != Tag {
						newTags = append(newTags, t)
					}
				}
				newTags = append(newTags, model.Tag{Name: "identify-failed"})
				item.Tags = newTags
				
				if err := w.store.UpdateItem(ctx, &item); err != nil {
					w.opts.Logger.Printf("[identify] failed to untag exhausted item %s: %v", item.ID, err)
				}
			}
			continue
		}
		w.clearAttempts(item.ID)
		w.opts.Logger.Printf("[identify] identified item %s", item.ID)
	}
}

func (w *Worker) processOne(ctx context.Context, item *model.Item, key string) error {
	media, err := w.collectMedia(item)
	if err != nil {
		return err
	}
	if len(media) == 0 {
		return errors.New("identify: no readable media for item")
	}

	// In-flight Gemini call gets its own ctx — independent of
	// the worker / shutdown context. SIGTERM during this call
	// lets it finish rather than abandoning a paid request.
	callCtx, cancel := context.WithTimeout(context.Background(), w.opts.CallTimeout)
	defer cancel()

	result, err := w.gem.Identify(callCtx, key, media, gemini.DefaultIdentifyPrompt)

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

func (w *Worker) collectMedia(item *model.Item) ([]gemini.Media, error) {
	var media []gemini.Media
	if item.ContentHash != "" {
		data, err := readBlob(w.fs, item.ContentHash)
		if err != nil {
			return nil, err
		}
		mime := mimeOr(item.MimeType, "image/jpeg")
		if strings.HasPrefix(mime, "video/") {
			// Cost Control: skip videos longer than 30 minutes to prevent
			// accidental massive bills. 30m is the plan's suggested limit.
			dur, err := getVideoDuration(data)
			if err == nil && dur > 30*time.Minute {
				return nil, fmt.Errorf("video too long for AI identify (%v > 30m)", dur)
			}
			if err != nil {
				w.opts.Logger.Printf("[identify] duration check failed for %s: %v", item.ID, err)
			}

			if item.HasTag("identify-lite") {
				audio, err := extractAudio(data)
				if err == nil && len(audio) > 0 {
					w.opts.Logger.Printf("[identify] lite-mode: extracted %d bytes audio from video %s", len(audio), item.ID)
					media = append(media, gemini.Media{
						Data:     audio,
						MimeType: "audio/mp3",
					})
				} else {
					w.opts.Logger.Printf("[identify] lite-mode audio extract failed for %s (fallback to full video): %v", item.ID, err)
					media = append(media, gemini.Media{
						Data:     data,
						MimeType: mime,
					})
				}
			} else {
				// Default: Multimodal (Full Video)
				media = append(media, gemini.Media{
					Data:     data,
					MimeType: mime,
				})
			}
		} else {
			media = append(media, gemini.Media{
				Data:     data,
				MimeType: mime,
			})
		}
	}
	for _, f := range item.Files {
		if f.ContentHash == "" {
			continue
		}
		data, err := readBlob(w.fs, f.ContentHash)
		if err != nil {
			w.opts.Logger.Printf("[identify] skipping attached file %d on item %s: %v", f.ID, item.ID, err)
			continue
		}
		fMime := mimeOr(f.MimeType, "image/jpeg")
		if strings.HasPrefix(fMime, "video/") {
			// Duration check for attached files too
			dur, err := getVideoDuration(data)
			if err == nil && dur > 30*time.Minute {
				w.opts.Logger.Printf("[identify] skipping attached video %d on %s: too long (%v)", f.ID, item.ID, dur)
				continue
			}

			if item.HasTag("identify-lite") {
				audio, err := extractAudio(data)
				if err == nil && len(audio) > 0 {
					w.opts.Logger.Printf("[identify] lite-mode: extracted %d bytes audio from attached video %d on %s", len(audio), f.ID, item.ID)
					media = append(media, gemini.Media{
						Data:     audio,
						MimeType: "audio/mp3",
					})
				} else {
					w.opts.Logger.Printf("[identify] lite-mode audio extract failed for attached file %d on item %s: %v", f.ID, item.ID, err)
					media = append(media, gemini.Media{
						Data:     data,
						MimeType: fMime,
					})
				}
			} else {
				media = append(media, gemini.Media{
					Data:     data,
					MimeType: fMime,
				})
			}
		} else {
			media = append(media, gemini.Media{
				Data:     data,
				MimeType: fMime,
			})
		}
	}
	return media, nil
}

func getVideoDuration(video []byte) (time.Duration, error) {
	tmpFile, err := os.CreateTemp("", "stash-duration-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(video); err != nil {
		return 0, err
	}
	tmpFile.Close()

	// -v error: quiet
	// -show_entries format=duration: only show duration
	// -of default=noprint_wrappers=1:nokey=1: just the number
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", tmpFile.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("ffprobe error: %v, out: %s", err, string(out))
	}

	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %v", string(out), err)
	}

	return time.Duration(seconds * float64(time.Second)), nil
}

func extractAudio(video []byte) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "stash-audio-ext-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	vidPath := filepath.Join(tmpDir, "vid")
	audPath := filepath.Join(tmpDir, "aud.mp3")

	if err := os.WriteFile(vidPath, video, 0600); err != nil {
		return nil, err
	}

	// -vn: no video
	// -acodec libmp3lame: convert to mp3
	// -q:a 5: decent quality VBR
	// -y: overwrite
	cmd := exec.Command("ffmpeg", "-y", "-i", vidPath, "-vn", "-acodec", "libmp3lame", "-q:a", "5", audPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg error: %v, out: %s", err, string(out))
	}

	return os.ReadFile(audPath)
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
