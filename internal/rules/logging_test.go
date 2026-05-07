package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendEvent_CreatesAndAppends(t *testing.T) {
	dir := t.TempDir()
	path := DefaultLogPath(dir)

	first := Event{
		Type:   EventFire,
		Rules:  []string{"youtube"},
		ItemID: "01ABCDEF",
		Title:  "Some video",
		Source: "https://youtube.com/watch?v=x",
		Effects: []string{"tags:video,watch-later"},
	}
	if err := AppendEvent(path, first); err != nil {
		t.Fatalf("first append: %v", err)
	}

	second := Event{
		Type:   EventSkip,
		Rules:  []string{"drop-spam"},
		Title:  "Junk",
		Source: "https://junk.example/",
	}
	if err := AppendEvent(path, second); err != nil {
		t.Fatalf("second append: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", len(lines), string(data))
	}
	if !strings.Contains(lines[0], `"type":"fire"`) {
		t.Errorf("first line missing type=fire: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"type":"skip"`) {
		t.Errorf("second line missing type=skip: %s", lines[1])
	}
}

func TestAppendEvent_FillsTimestampWhenZero(t *testing.T) {
	dir := t.TempDir()
	path := DefaultLogPath(dir)

	if err := AppendEvent(path, Event{Type: EventFire, Rules: []string{"r"}, Title: "t", Source: "s"}); err != nil {
		t.Fatal(err)
	}
	events, err := ReadEvents(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].Timestamp.IsZero() {
		t.Errorf("timestamp not auto-filled")
	}
	// Should be very recent (last 5 seconds).
	if time.Since(events[0].Timestamp) > 5*time.Second {
		t.Errorf("timestamp older than expected: %v", events[0].Timestamp)
	}
}

func TestReadEvents_NewestFirst(t *testing.T) {
	dir := t.TempDir()
	path := DefaultLogPath(dir)

	for i := 0; i < 3; i++ {
		ev := Event{
			Type:      EventFire,
			Rules:     []string{"r"},
			Title:     "ev" + string(rune('0'+i)),
			Source:    "s",
			Timestamp: time.Date(2026, 5, 6, 12, 0, i, 0, time.UTC),
		}
		if err := AppendEvent(path, ev); err != nil {
			t.Fatal(err)
		}
	}

	events, err := ReadEvents(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}
	if events[0].Title != "ev2" || events[2].Title != "ev0" {
		t.Errorf("events not newest-first: %+v", events)
	}
}

func TestReadEvents_LimitTruncates(t *testing.T) {
	dir := t.TempDir()
	path := DefaultLogPath(dir)
	for i := 0; i < 5; i++ {
		_ = AppendEvent(path, Event{Type: EventFire, Rules: []string{"r"}, Title: "t", Source: "s"})
	}
	events, err := ReadEvents(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Errorf("want 2 events with limit, got %d", len(events))
	}
}

func TestReadEvents_MissingFileEmpty(t *testing.T) {
	events, err := ReadEvents("/tmp/nonexistent-rules-log.jsonl", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("expected empty events, got %d", len(events))
	}
}

func TestReadEvents_SkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := DefaultLogPath(dir)
	// Write valid event + a junk line + another valid event manually.
	data := `{"timestamp":"2026-05-06T12:00:00Z","type":"fire","rules":["r"],"title":"a","source":"s"}
NOT JSON AT ALL
{"timestamp":"2026-05-06T12:00:01Z","type":"skip","rules":["r2"],"title":"b","source":"s2"}
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	events, err := ReadEvents(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Errorf("want 2 valid events, got %d: %+v", len(events), events)
	}
}

func TestFormatEffects(t *testing.T) {
	r := Result{
		Tags:         []string{"video", "watch-later"},
		Collection:   "bills",
		Title:        "Invoice $42.00",
		AppendedNote: "Detected total: $42.00",
		Notifies:     []string{"Hey", "There"},
		Links: []LinkSpec{
			{Tag: "research"},
			{ID: "01ABCDEFGHIJKLMNOP"},
		},
	}
	got := FormatEffects(r)
	want := []string{
		"tags:video,watch-later",
		"coll:bills",
		"title:Invoice $42.00",
		"note+:Detected total: $42.00",
		"notify×2",
		"link:#research",
		"link:01ABCDEF",
	}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %d, want %d:\ngot=%v\nwant=%v", len(got), len(want), got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] got %q, want %q", i, got[i], w)
		}
	}
}

func TestFormatEffects_EmptyResult(t *testing.T) {
	if got := FormatEffects(Result{}); len(got) != 0 {
		t.Errorf("empty result should produce no effects, got %v", got)
	}
}

func TestFormatEffects_TruncatesLongTitle(t *testing.T) {
	long := strings.Repeat("X", 200)
	r := Result{Title: long}
	got := FormatEffects(r)
	if len(got) != 1 {
		t.Fatalf("want 1 effect, got %d", len(got))
	}
	if !strings.HasPrefix(got[0], "title:") {
		t.Errorf("expected title prefix: %q", got[0])
	}
	if !strings.HasSuffix(got[0], "…") {
		t.Errorf("expected trailing ellipsis on long title: %q", got[0])
	}
}

func TestMigrate_SkipLogToRulesLog(t *testing.T) {
	dir := t.TempDir()
	skipPath := LegacySkipLogPath(dir)
	rulesPath := DefaultLogPath(dir)

	legacy := `2026-05-06T12:29:28Z	rule=drop-junk	type=link	title="Spam page"	source=https://junk.example/spam
2026-05-06T12:30:00Z	rule=drop-mail	type=email	title="Promo"	source=<email>
malformed line that should be skipped
`
	if err := os.WriteFile(skipPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	// Trigger migration via a real append.
	newEvent := Event{Type: EventFire, Rules: []string{"r"}, Title: "post-migration", Source: "s"}
	if err := AppendEvent(rulesPath, newEvent); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Legacy file gone.
	if _, err := os.Stat(skipPath); !os.IsNotExist(err) {
		t.Errorf("skip.log should have been removed: %v", err)
	}

	// Rules.log has both migrated entries plus the new event.
	events, err := ReadEvents(rulesPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("want 3 events (2 migrated + 1 new), got %d:\n%+v", len(events), events)
	}

	// Newest first: new event first.
	if events[0].Title != "post-migration" {
		t.Errorf("first event should be the new one, got %q", events[0].Title)
	}
	// Then the migrated skip events (order preserved within migration).
	if events[1].Type != EventSkip || events[1].Title != "Promo" {
		t.Errorf("event[1] = %+v", events[1])
	}
	if events[2].Type != EventSkip || events[2].Title != "Spam page" {
		t.Errorf("event[2] = %+v", events[2])
	}
}

func TestMigrate_NoSkipLogIsNoOp(t *testing.T) {
	dir := t.TempDir()
	rulesPath := DefaultLogPath(dir)
	// No skip.log exists.
	if err := AppendEvent(rulesPath, Event{Type: EventFire, Rules: []string{"r"}, Title: "t", Source: "s"}); err != nil {
		t.Fatal(err)
	}
	events, err := ReadEvents(rulesPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("want 1 event, got %d", len(events))
	}
}

func TestParseLegacySkipLine(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		ok    bool
		want  Event
	}{
		{
			name: "happy path",
			line: "2026-05-06T12:29:28Z\trule=drop-junk\ttype=link\ttitle=\"Spam\"\tsource=https://x.com",
			ok:   true,
			want: Event{
				Type:   EventSkip,
				Rules:  []string{"drop-junk"},
				Title:  "Spam",
				Source: "https://x.com",
			},
		},
		{
			name: "blank line",
			line: "",
			ok:   false,
		},
		{
			name: "no rule= field is rejected",
			line: "2026-05-06T12:29:28Z\ttitle=\"Floating\"",
			ok:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseLegacySkipLine(tc.line)
			if ok != tc.ok {
				t.Errorf("ok = %v, want %v (got %+v)", ok, tc.ok, got)
				return
			}
			if !ok {
				return
			}
			if got.Type != tc.want.Type ||
				len(got.Rules) != len(tc.want.Rules) ||
				got.Title != tc.want.Title ||
				got.Source != tc.want.Source {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Sanity check: log path helpers compose as expected.
func TestMigrateRulesLog(t *testing.T) {
	dir := t.TempDir()
	rulesPath := LegacyRulesLogPath(dir)
	capturePath := DefaultLogPath(dir)

	// Seed legacy rules.log with two events.
	legacy := `{"timestamp":"2026-04-01T10:00:00Z","type":"fire","rules":["youtube"],"item_id":"01A","title":"Vid","source":"https://youtu.be/x"}` + "\n" +
		`{"timestamp":"2026-04-02T10:00:00Z","type":"skip","rules":["spam"],"title":"Junk","source":"http://spam.example"}` + "\n"
	if err := os.WriteFile(rulesPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	// Append a fresh event to capture.log; the migration should
	// happen as a side-effect and the result should hold all three
	// events.
	if err := AppendEvent(capturePath, Event{
		Type:   EventCapture,
		ItemID: "01B",
		Title:  "Untriaged",
		Source: "https://example.com",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Legacy file should be gone.
	if _, err := os.Stat(rulesPath); !os.IsNotExist(err) {
		t.Errorf("rules.log should have been removed after migration; err=%v", err)
	}

	events, err := ReadEvents(capturePath, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events after migration, got %d", len(events))
	}
	// Newest first: the just-appended capture, then the two from
	// the legacy file in their original order (ReadEvents reverses).
	if events[0].Type != EventCapture || events[0].ItemID != "01B" {
		t.Errorf("newest event should be the capture; got %+v", events[0])
	}
}

func TestPathHelpers(t *testing.T) {
	got := DefaultLogPath("/tmp/stash")
	if got != filepath.Join("/tmp/stash", "capture.log") {
		t.Errorf("DefaultLogPath = %q", got)
	}
	got = LegacyRulesLogPath("/tmp/stash")
	if got != filepath.Join("/tmp/stash", "rules.log") {
		t.Errorf("LegacyRulesLogPath = %q", got)
	}
	got = LegacySkipLogPath("/tmp/stash")
	if got != filepath.Join("/tmp/stash", "skip.log") {
		t.Errorf("LegacySkipLogPath = %q", got)
	}
}
