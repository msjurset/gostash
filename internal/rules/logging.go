package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EventType discriminates entries in the rules log.
type EventType string

const (
	// EventFire — capture-time: at least one rule matched and the item
	// was saved. `Rules` lists every rule that matched; `Effects` is a
	// flat summary of the composed actions.
	EventFire EventType = "fire"

	// EventSkip — capture-time: a rule with `skip: true` matched and
	// aborted the add. `Rules[0]` is the rule that fired the skip; the
	// item was never persisted (no ItemID).
	EventSkip EventType = "skip"

	// EventRetro — `stash rules apply` produced a change on an existing
	// item. Same shape as EventFire but represents a retroactive run
	// rather than a capture.
	EventRetro EventType = "retro"

	// EventCapture — capture-time: the item was saved but no rule
	// matched. The unified-log answer to "what got stashed today
	// that fell through every rule?". `Rules` is empty.
	EventCapture EventType = "capture"

	// EventError — capture failed before the item could be saved
	// (fetch error, parse error, file I/O, etc.). No ItemID; the
	// `Error` field carries the message and `Source` carries
	// whatever identifying string the call site had.
	EventError EventType = "error"

	// EventMerge — one or more items were folded into a target via
	// `stash merge` (CLI) or the equivalent HTTP endpoint. `ItemID`
	// is the SURVIVING target's ID; `Sources` carries the merged-in
	// source IDs in their original argument order. Surfaces in
	// provenance / activity views so the user can see when a row
	// absorbed others (the source rows' own log entries become
	// orphans — they still exist on disk but no longer reference a
	// live item, which the provenance reader filters out by item ID).
	EventMerge EventType = "merge"
)

// Event is one entry in $STASH_DIR/rules.log. JSON-encoded; one line per
// event. Fields use `omitempty` so the file stays compact for skip events
// (which have no item / effects).
type Event struct {
	Timestamp time.Time `json:"timestamp"`
	Type      EventType `json:"type"`
	Rules     []string  `json:"rules"`
	ItemID    string    `json:"item_id,omitempty"`
	Title     string    `json:"title"`
	Source    string    `json:"source"`
	Effects   []string  `json:"effects,omitempty"`
	// Error is populated only on EventError entries — the original
	// error message that caused the capture to fail. `omitempty` so
	// the existing event types stay byte-for-byte identical on disk.
	Error string `json:"error,omitempty"`
	// Sources carries the source-item IDs on EventMerge entries —
	// the items that were folded into the surviving ItemID. Empty
	// for every other event type and omitted on serialization so
	// existing log files round-trip identically.
	Sources []string `json:"sources,omitempty"`
}

// DefaultLogPath returns the canonical capture.log path:
// $STASH_DIR/capture.log. The file records every successful capture
// (with or without rule matches), every skip, every retroactive rule
// fire, and every ingest error — a unified audit trail across all
// ingest surfaces.
func DefaultLogPath(stashDir string) string {
	return filepath.Join(stashDir, "capture.log")
}

// LegacyRulesLogPath returns the previous "rules-only" log location.
// Folded into the unified capture.log on first append; see
// migrate_capture_log.go.
func LegacyRulesLogPath(stashDir string) string {
	return filepath.Join(stashDir, "rules.log")
}

// LegacySkipLogPath returns the original skip-only log location.
// Predates rules.log; folded into rules.log first, then migrated
// onward to capture.log.
func LegacySkipLogPath(stashDir string) string {
	return filepath.Join(stashDir, "skip.log")
}

// AppendEvent serializes `ev` and appends one line to `path`. Uses
// `O_APPEND` so concurrent processes' writes don't tear (POSIX guarantees
// atomic append for writes ≤ PIPE_BUF, which a one-line event easily fits).
//
// If a legacy skip.log exists at the same directory, it's silently migrated
// into the unified rules.log before the new event is written. The legacy
// file is removed on successful migration; subsequent appends skip the
// check (the file is gone). Any migration failure logs to stderr and
// continues — rule events should never fail because the log writer is
// flaky.
func AppendEvent(path string, ev Event) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}

	// Best-effort migrations from the two prior log files. Each
	// runs only if the legacy file is still present; both remove
	// the legacy file on success so subsequent appends skip the
	// check entirely. Failures log to stderr but never escalate —
	// rule events should never be blocked by a flaky log writer.
	dir := filepath.Dir(path)
	if legacy := LegacySkipLogPath(dir); fileExists(legacy) {
		if err := migrateLegacySkipLog(legacy, path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: migrate skip.log: %v\n", err)
		}
	}
	if legacy := LegacyRulesLogPath(dir); fileExists(legacy) && legacy != path {
		if err := migrateLegacyRulesLog(legacy, path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: migrate rules.log: %v\n", err)
		}
	}

	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
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

// ReadEvents parses up to `limit` of the most recent events from `path`.
// Returns events in newest-first order. Malformed lines are skipped (the
// log is append-only so partial / corrupt entries are unusual but not
// catastrophic). Pass `limit = 0` to return everything.
//
// Also runs the legacy-log migrations on entry so a `stash log` against
// a stash dir that was last touched before the rename still surfaces
// the historical events without the user having to capture something
// first to trigger AppendEvent's migration path.
func ReadEvents(path string, limit int) ([]Event, error) {
	dir := filepath.Dir(path)
	if legacy := LegacySkipLogPath(dir); fileExists(legacy) {
		if err := migrateLegacySkipLog(legacy, path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: migrate skip.log: %v\n", err)
		}
	}
	if legacy := LegacyRulesLogPath(dir); fileExists(legacy) && legacy != path {
		if err := migrateLegacyRulesLog(legacy, path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: migrate rules.log: %v\n", err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	out := make([]Event, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // malformed — skip
		}
		out = append(out, ev)
	}
	// Newest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// FormatEffects produces compact one-string-per-effect summaries from a
// rule application result. Used by the activity log so consumers can
// render action chips without needing the full Result structure.
//
// Format examples:
//
//	tags:video,watch-later
//	coll:bills
//	title:Invoice $42.00
//	note+:Detected total: $42.00
//	notify×2
//	link:#research
//	link:01ABCDEF
func FormatEffects(result Result) []string {
	var out []string
	if len(result.Tags) > 0 {
		out = append(out, "tags:"+strings.Join(result.Tags, ","))
	}
	if result.Collection != "" {
		out = append(out, "coll:"+result.Collection)
	}
	if result.Title != "" {
		out = append(out, "title:"+truncate(result.Title, 60))
	}
	if result.Note != "" {
		out = append(out, "note:"+truncate(result.Note, 60))
	}
	if result.AppendedNote != "" {
		out = append(out, "note+:"+truncate(result.AppendedNote, 60))
	}
	if len(result.Notifies) > 0 {
		if len(result.Notifies) == 1 {
			out = append(out, "notify")
		} else {
			out = append(out, fmt.Sprintf("notify×%d", len(result.Notifies)))
		}
	}
	for _, l := range result.Links {
		switch {
		case l.Tag != "":
			out = append(out, "link:#"+l.Tag)
		case l.ID != "":
			n := len(l.ID)
			if n > 8 {
				n = 8
			}
			out = append(out, "link:"+l.ID[:n])
		}
	}
	if t := result.Thumbnail; t != nil {
		switch {
		case t.From != "":
			out = append(out, "thumb:"+truncate(t.From, 40))
		case t.Auto:
			out = append(out, "thumb:auto")
		}
		}
		if len(result.Execs) > 0 {
		if len(result.Execs) == 1 {
			out = append(out, "exec")
		} else {
			out = append(out, fmt.Sprintf("exec×%d", len(result.Execs)))
		}
		}
		return out
		}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Truncate by runes, not bytes, to avoid splitting a multibyte char.
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}
