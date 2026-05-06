package main

import (
	"testing"
	"time"

	"github.com/msjurset/gostash/internal/rules"
)

func TestParseLogDuration(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"hours", "2h", 2 * time.Hour},
		{"minutes", "30m", 30 * time.Minute},
		{"days", "7d", 7 * 24 * time.Hour},
		{"weeks", "2w", 14 * 24 * time.Hour},
		{"compound", "1h30m", 90 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLogDuration(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseLogDuration_BadInput(t *testing.T) {
	_, err := parseLogDuration("garbage")
	if err == nil {
		t.Error("expected error on garbage input")
	}
	_, err = parseLogDuration("xd")
	if err == nil {
		t.Error("expected error on non-numeric days")
	}
}

func TestValidEventType(t *testing.T) {
	for _, ok := range []string{"fire", "skip", "retro"} {
		if !validEventType(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "Fire", "info", "warn"} {
		if validEventType(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestFilterEvents(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	events := []rules.Event{
		{Timestamp: now, Type: rules.EventFire, Rules: []string{"yt", "web"}, Title: "video"},
		{Timestamp: now.Add(-2 * time.Hour), Type: rules.EventSkip, Rules: []string{"drop-spam"}, Title: "spam"},
		{Timestamp: now.Add(-2 * 24 * time.Hour), Type: rules.EventRetro, Rules: []string{"yt"}, Title: "old"},
	}

	t.Run("no filter", func(t *testing.T) {
		got := filterEvents(events, "", "", time.Time{})
		if len(got) != 3 {
			t.Errorf("want 3, got %d", len(got))
		}
	})

	t.Run("type filter", func(t *testing.T) {
		got := filterEvents(events, "fire", "", time.Time{})
		if len(got) != 1 || got[0].Title != "video" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("rule filter matches any in list", func(t *testing.T) {
		got := filterEvents(events, "", "web", time.Time{})
		if len(got) != 1 || got[0].Title != "video" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("since filter excludes older", func(t *testing.T) {
		got := filterEvents(events, "", "", now.Add(-1*time.Hour))
		if len(got) != 1 || got[0].Title != "video" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("compound filter (type AND rule)", func(t *testing.T) {
		got := filterEvents(events, "retro", "yt", time.Time{})
		if len(got) != 1 || got[0].Title != "old" {
			t.Errorf("got %+v", got)
		}
	})
}

func TestTruncateForTable(t *testing.T) {
	if got := truncateForTable("short", 10); got != "short" {
		t.Errorf("short string mangled: %q", got)
	}
	long := "this is a very long title that should get truncated"
	got := truncateForTable(long, 20)
	if got == long {
		t.Errorf("long string not truncated")
	}
	r := []rune(got)
	if len(r) != 20 || r[len(r)-1] != '…' {
		t.Errorf("truncated string wrong shape: %q (len=%d)", got, len(r))
	}
}
