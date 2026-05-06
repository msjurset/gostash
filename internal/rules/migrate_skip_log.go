package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// migrateLegacySkipLog folds the previous-format skip.log into the unified
// rules.log. Best-effort: malformed lines are silently dropped (no
// failure for one bad entry). The legacy file is removed on success so
// subsequent calls skip migration entirely.
//
// Old format (tab-separated):
//
//	2026-05-06T12:29:28Z\trule=NAME\ttype=link\ttitle="Spam"\tsource=https://...
//
// New format: one EventSkip per line in the rules log.
func migrateLegacySkipLog(legacyPath, newPath string) error {
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", legacyPath, err)
	}

	var events []Event
	for _, line := range strings.Split(string(data), "\n") {
		ev, ok := parseLegacySkipLine(line)
		if !ok {
			continue
		}
		events = append(events, ev)
	}

	if len(events) > 0 {
		if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
			return fmt.Errorf("mkdir: %w", err)
		}
		f, err := os.OpenFile(newPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open %s: %w", newPath, err)
		}
		for _, ev := range events {
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := f.Write(append(b, '\n')); err != nil {
				f.Close()
				return fmt.Errorf("write migrated event: %w", err)
			}
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close %s: %w", newPath, err)
		}
	}

	if err := os.Remove(legacyPath); err != nil {
		// Migration succeeded but cleanup failed — log to stderr but
		// don't fail. Worst case we re-migrate next time (idempotent
		// since events would just append; not great but recoverable).
		fmt.Fprintf(os.Stderr, "warning: migrated %s but couldn't remove it: %v\n",
			legacyPath, err)
	}
	return nil
}

// parseLegacySkipLine parses one tab-separated skip.log line into an
// Event. Returns ok=false for blank or unparseable lines.
func parseLegacySkipLine(line string) (Event, bool) {
	if strings.TrimSpace(line) == "" {
		return Event{}, false
	}
	parts := strings.Split(line, "\t")
	if len(parts) < 2 {
		return Event{}, false
	}

	ev := Event{Type: EventSkip}
	if ts, err := time.Parse(time.RFC3339, parts[0]); err == nil {
		ev.Timestamp = ts
	}

	for _, p := range parts[1:] {
		switch {
		case strings.HasPrefix(p, "rule="):
			ev.Rules = []string{strings.TrimPrefix(p, "rule=")}
		case strings.HasPrefix(p, "title="):
			t := strings.TrimPrefix(p, "title=")
			t = strings.Trim(t, `"`)
			ev.Title = t
		case strings.HasPrefix(p, "source="):
			ev.Source = strings.TrimPrefix(p, "source=")
		}
	}

	if len(ev.Rules) == 0 {
		// Without a rule reference the entry is meaningless.
		return Event{}, false
	}
	return ev, true
}
