// Package embed provides the background worker that generates
// vector embeddings for items using the Gemini API.
package embed

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/msjurset/gostash/internal/credentials"
	"github.com/msjurset/gostash/internal/gemini"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"
)

// UsageRecorder matches the one in identify/worker.go
type UsageRecorder interface {
	Record(model string, promptTokens, candidatesTokens int)
}

type noopRecorder struct{}

func (noopRecorder) Record(string, int, int) {}

// Options control the worker's polling cadence and batching.
type Options struct {
	PollInterval time.Duration
	BatchSize    int
	MaxAttempts  int
	CallTimeout  time.Duration
	Recorder     UsageRecorder
	Logger       *log.Logger
}

func (o *Options) applyDefaults() {
	if o.PollInterval <= 0 {
		o.PollInterval = 60 * time.Second // Embeddings are less urgent than identification
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 10
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 3
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

// Worker is the polling embedding worker.
type Worker struct {
	store store.Store
	gem   *gemini.Client
	opts  Options

	mu              sync.Mutex
	attempts        map[string]int
	lastRejectedKey string
	lastKeyLogged   bool
}

// New constructs a worker.
func New(s store.Store, gem *gemini.Client, opts Options) *Worker {
	opts.applyDefaults()
	return &Worker{
		store:    s,
		gem:      gem,
		opts:     opts,
		attempts: make(map[string]int),
	}
}

// Run blocks the calling goroutine until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.opts.PollInterval)
	defer ticker.Stop()

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
		w.opts.Logger.Printf("[embed] keychain load error: %v", err)
		return
	}
	if key == "" {
		if w.lastKeyLogged {
			w.opts.Logger.Printf("[embed] no Gemini key cached — idling")
			w.lastKeyLogged = false
		}
		return
	}
	if !w.lastKeyLogged {
		w.opts.Logger.Printf("[embed] Gemini key available; worker active")
		w.lastKeyLogged = true
	}

	w.mu.Lock()
	rejected := w.lastRejectedKey == key && key != ""
	w.mu.Unlock()
	if rejected {
		return
	}

	items, err := w.store.ListItemsMissingEmbeddings(ctx, w.opts.BatchSize)
	if err != nil {
		w.opts.Logger.Printf("[embed] list pending items: %v", err)
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

		if err := w.processOne(ctx, &item, key); err != nil {
			if gemini.IsKeyRejected(err) {
				w.mu.Lock()
				w.lastRejectedKey = key
				w.mu.Unlock()
				w.opts.Logger.Printf("[embed] Gemini rejected key (%v). Worker paused.", err)
				return
			}

			n := w.bumpAttempts(item.ID)
			w.opts.Logger.Printf("[embed] error on item %s (attempt %d/%d): %v", item.ID, n, w.opts.MaxAttempts, err)
			continue
		}
		w.clearAttempts(item.ID)
		w.opts.Logger.Printf("[embed] embedded item %s", item.ID)
	}
}

func (w *Worker) processOne(ctx context.Context, item *model.Item, key string) error {
	// Construct text to embed: Title + Notes + ExtractedText
	var parts []string
	if item.Title != "" {
		parts = append(parts, item.Title)
	}
	if item.Notes != "" {
		parts = append(parts, item.Notes)
	}
	if item.ExtractedText != "" {
		parts = append(parts, item.ExtractedText)
	}
	text := strings.Join(parts, "\n\n")
	if strings.TrimSpace(text) == "" {
		// Nothing to embed, save a zero vector or skip?
		// Better to save something so we don't keep polling it.
		text = "empty item"
	}

	callCtx, cancel := context.WithTimeout(context.Background(), w.opts.CallTimeout)
	defer cancel()

	result, err := w.gem.EmbedContent(callCtx, key, text)
	if result.Model != "" {
		// Record usage (input tokens)
		w.opts.Recorder.Record(result.Model, result.Tokens, 0)
	}

	if err != nil {
		return err
	}

	return w.store.SaveItemEmbedding(ctx, item.ID, result.Model, result.Vector)
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
