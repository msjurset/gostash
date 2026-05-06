package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/rules"
	"github.com/msjurset/gostash/internal/store"
)

// memStore creates a fresh in-memory SQLite store for a single test. The
// store layer's own tests use the same `:memory:` DSN trick.
func memStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func makeItem(id string, itemType model.ItemType, title string, tags []string) *model.Item {
	now := time.Now().UTC()
	item := &model.Item{
		ID:        id,
		Type:      itemType,
		Title:     title,
		URL:       "https://example.com/" + id,
		Metadata:  json.RawMessage("{}"),
		CreatedAt: now,
		UpdatedAt: now,
	}
	for _, t := range tags {
		item.Tags = append(item.Tags, model.Tag{Name: t})
	}
	return item
}

func TestApplyLinkAction_ByTag(t *testing.T) {
	s := memStore(t)
	ctx := context.Background()

	// Two pre-existing items tagged "alpha"
	if err := s.CreateItem(ctx, makeItem("01ALPHA1AAAAAAAAAAAAAAAAAA", model.TypeURL, "Alpha 1", []string{"alpha"})); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateItem(ctx, makeItem("01ALPHA2AAAAAAAAAAAAAAAAAA", model.TypeURL, "Alpha 2", []string{"alpha"})); err != nil {
		t.Fatal(err)
	}
	// One unrelated item
	if err := s.CreateItem(ctx, makeItem("01OTHER1AAAAAAAAAAAAAAAAAA", model.TypeURL, "Other", []string{"beta"})); err != nil {
		t.Fatal(err)
	}

	source := makeItem("01SOURCE1AAAAAAAAAAAAAAAAA", model.TypeURL, "Source", nil)
	if err := s.CreateItem(ctx, source); err != nil {
		t.Fatal(err)
	}

	applyLinkAction(ctx, s, source, rules.LinkSpec{Tag: "alpha"})

	links, err := s.ListLinks(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Errorf("expected 2 links, got %d: %+v", len(links), links)
	}
	for _, l := range links {
		if !strings.HasPrefix(l.Title, "Alpha") {
			t.Errorf("unexpected link target %q", l.Title)
		}
	}
}

func TestApplyLinkAction_ByID(t *testing.T) {
	s := memStore(t)
	ctx := context.Background()

	target := makeItem("01TARGET1AAAAAAAAAAAAAAAAA", model.TypeURL, "Target", nil)
	if err := s.CreateItem(ctx, target); err != nil {
		t.Fatal(err)
	}
	source := makeItem("01SOURCE1AAAAAAAAAAAAAAAAA", model.TypeURL, "Source", nil)
	if err := s.CreateItem(ctx, source); err != nil {
		t.Fatal(err)
	}

	applyLinkAction(ctx, s, source, rules.LinkSpec{ID: target.ID})

	links, err := s.ListLinks(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ItemID != target.ID {
		t.Errorf("expected one link to %s, got %+v", target.ID, links)
	}
}

func TestApplyLinkAction_SkipsSelf(t *testing.T) {
	s := memStore(t)
	ctx := context.Background()

	source := makeItem("01SELF11AAAAAAAAAAAAAAAAAA", model.TypeURL, "Self", []string{"alpha"})
	if err := s.CreateItem(ctx, source); err != nil {
		t.Fatal(err)
	}

	applyLinkAction(ctx, s, source, rules.LinkSpec{Tag: "alpha"})

	links, err := s.ListLinks(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Errorf("self-link should have been suppressed, got %+v", links)
	}
}

func TestNotificationClickTarget(t *testing.T) {
	tests := []struct {
		name string
		item *model.Item
		want string
	}{
		{
			name: "url item",
			item: &model.Item{Type: model.TypeURL, URL: "https://example.com/x"},
			want: "https://example.com/x",
		},
		{
			name: "snippet has no target",
			item: &model.Item{Type: model.TypeSnippet, ExtractedText: "some text"},
			want: "",
		},
		{
			name: "email has no target (just text)",
			item: &model.Item{Type: model.TypeEmail, ExtractedText: "From: foo"},
			want: "",
		},
		{
			name: "file with missing source path falls back to no target",
			item: &model.Item{Type: model.TypeFile, SourcePath: "/tmp/definitely-not-here-123abc.xyz"},
			want: "",
		},
		{
			name: "nil is empty",
			item: nil,
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := notificationClickTarget(tc.item); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
