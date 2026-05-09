package audit

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndRead(t *testing.T) {
	dir := t.TempDir()
	path := DefaultTagsLogPath(dir)

	events := []TagEvent{
		{Action: ActionAdd, Tag: "video", ItemID: "01A", ItemType: "url", ItemURL: "https://www.youtube.com/watch?v=x", Source: "edit"},
		{Action: ActionAdd, Tag: "video", ItemID: "01B", ItemType: "url", ItemURL: "https://youtube.com/watch?v=y", Source: "edit"},
		{Action: ActionRemove, Tag: "tmp", ItemID: "01C", ItemType: "snippet", Source: "bulk"},
	}
	for _, ev := range events {
		if err := AppendTagEvent(path, ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	got, err := ReadTagEvents(path, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(events) {
		t.Fatalf("want %d events, got %d", len(events), len(got))
	}
	// Newest-first; last appended is first read.
	if got[0].ItemID != "01C" {
		t.Errorf("expected newest-first; got[0].ItemID = %q, want %q", got[0].ItemID, "01C")
	}
	// Domain auto-derivation, www stripping.
	for _, ev := range got {
		if ev.ItemURL == "" {
			continue
		}
		if ev.ItemDomain != "youtube.com" {
			t.Errorf("expected youtube.com domain, got %q (url %q)", ev.ItemDomain, ev.ItemURL)
		}
	}
}

func TestReadLimit(t *testing.T) {
	dir := t.TempDir()
	path := DefaultTagsLogPath(dir)
	for i := 0; i < 10; i++ {
		_ = AppendTagEvent(path, TagEvent{Action: ActionAdd, Tag: "x", ItemID: "id-" + string(rune('a'+i))})
	}
	got, err := ReadTagEvents(path, 3)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("limit=3 returned %d events", len(got))
	}
}

func TestReadMissingFile(t *testing.T) {
	got, err := ReadTagEvents(filepath.Join(t.TempDir(), "does-not-exist.log"), 0)
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("missing file should return empty slice")
	}
}

func TestTimestampDefault(t *testing.T) {
	dir := t.TempDir()
	path := DefaultTagsLogPath(dir)
	before := time.Now().UTC()
	_ = AppendTagEvent(path, TagEvent{Action: ActionAdd, Tag: "x", ItemID: "01A"})
	after := time.Now().UTC()
	got, _ := ReadTagEvents(path, 0)
	if len(got) != 1 {
		t.Fatalf("expected 1 event")
	}
	ts := got[0].Timestamp
	if ts.Before(before) || ts.After(after) {
		t.Errorf("timestamp %v not in [%v, %v]", ts, before, after)
	}
}

func TestExtractDomain(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=x":  "youtube.com",
		"http://example.com/path":            "example.com",
		"https://www.WORK.example.org/x":     "work.example.org",
		"":                                   "",
		"not a url":                          "",
		"file:///local":                      "",
	}
	for in, want := range cases {
		got := extractDomain(in)
		if got != want {
			t.Errorf("extractDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
