// Package audit records user-driven lifecycle events on stashed items
// for downstream analysis. Today: tag add/remove events. The append-only
// log feeds the rule-suggestion path (Apple Foundation Models reads the
// last N events, looks for tag/property co-occurrence patterns, proposes
// rules) and gives the user a browsable history of their own tagging.
//
// Capture-time effects already live in $STASH_DIR/capture.log (rule fires,
// skips, retroactive applies, captures). Tag mutations after capture are
// a different domain — they reflect how the user actively organizes the
// archive — so they live in a separate $STASH_DIR/tags.log to keep both
// schemas focused.
package audit

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TagAction discriminates tag mutation events.
type TagAction string

const (
	ActionAdd    TagAction = "add"
	ActionRemove TagAction = "remove"
)

// TagEvent records one tag mutation. URL/domain are snapshotted at
// log-time so subsequent edits to the item don't invalidate historical
// records (e.g. a URL update shouldn't rewrite past tag patterns).
type TagEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	Action     TagAction `json:"action"`
	Tag        string    `json:"tag"`
	ItemID     string    `json:"item_id"`
	ItemType   string    `json:"item_type,omitempty"`
	ItemURL    string    `json:"item_url,omitempty"`
	ItemDomain string    `json:"item_domain,omitempty"`
	// Source identifies the surface that triggered the mutation:
	// "edit"     — `stash edit --add-tag/--remove-tag`
	// "bulk"     — `stash bulk tag --add-tag/--remove-tag`
	// Rules-engine and capture-time tag application is intentionally
	// not logged here — those events already appear in capture.log
	// and aren't representative of "user actively tagging" behavior.
	Source string `json:"source,omitempty"`
}

// DefaultTagsLogPath returns $STASH_DIR/tags.log.
func DefaultTagsLogPath(stashDir string) string {
	return filepath.Join(stashDir, "tags.log")
}

// AppendTagEvent serializes ev and appends one NDJSON line to path.
// O_APPEND with a write under PIPE_BUF gives atomic append on POSIX
// even with multiple writers (rare, but possible across CLI + Mac
// app concurrent runs).
func AppendTagEvent(path string, ev TagEvent) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if ev.ItemDomain == "" && ev.ItemURL != "" {
		ev.ItemDomain = extractDomain(ev.ItemURL)
	}

	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode tag event: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ReadTagEvents parses up to limit of the most recent events from path,
// newest-first. Pass limit = 0 for all. Malformed lines are skipped.
func ReadTagEvents(path string, limit int) ([]TagEvent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	out := make([]TagEvent, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev TagEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func extractDomain(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	return host
}
