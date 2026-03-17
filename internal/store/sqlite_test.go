package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/msjurset/gostash/internal/model"
)

func testStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testItem(id string, itemType model.ItemType) *model.Item {
	now := time.Now().UTC()
	return &model.Item{
		ID:        id,
		Type:      itemType,
		Title:     "Test " + id,
		Notes:     "Some notes",
		Metadata:  json.RawMessage("{}"),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestCreateAndGetItem(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	item := testItem("01ABC", model.TypeSnippet)
	item.ExtractedText = "hello world"
	item.Tags = []model.Tag{{Name: "test"}, {Name: "golang"}}

	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetItem(ctx, "01ABC")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Title != "Test 01ABC" {
		t.Errorf("title = %q, want %q", got.Title, "Test 01ABC")
	}
	if got.Type != model.TypeSnippet {
		t.Errorf("type = %q, want %q", got.Type, model.TypeSnippet)
	}
	if len(got.Tags) != 2 {
		t.Errorf("tags = %d, want 2", len(got.Tags))
	}
}

func TestListItems(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for _, id := range []string{"01A", "01B", "01C"} {
		item := testItem(id, model.TypeLink)
		item.URL = "https://example.com/" + id
		if err := s.CreateItem(ctx, item); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	items, err := s.ListItems(ctx, model.ItemFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("got %d items, want 3", len(items))
	}
}

func TestListItemsFilterByType(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.CreateItem(ctx, testItem("01A", model.TypeLink)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateItem(ctx, testItem("01B", model.TypeSnippet)); err != nil {
		t.Fatal(err)
	}

	items, err := s.ListItems(ctx, model.ItemFilter{Type: model.TypeLink})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("got %d items, want 1", len(items))
	}
}

func TestSearchItems(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	item := testItem("01A", model.TypeSnippet)
	item.Title = "How to cook pasta"
	item.ExtractedText = "Boil water, add salt, cook for 8 minutes"
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	item2 := testItem("01B", model.TypeSnippet)
	item2.Title = "Go programming tips"
	item2.ExtractedText = "Use interfaces, handle errors, write tests"
	if err := s.CreateItem(ctx, item2); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		query string
		want  int
	}{
		{"pasta", 1},
		{"programming", 1},
		{"water salt", 1},
		{"nonexistent", 0},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			items, err := s.SearchItems(ctx, model.ItemFilter{Query: tt.query})
			if err != nil {
				t.Fatalf("search %q: %v", tt.query, err)
			}
			if len(items) != tt.want {
				t.Errorf("search %q: got %d, want %d", tt.query, len(items), tt.want)
			}
		})
	}
}

func TestUpdateItem(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	item := testItem("01A", model.TypeSnippet)
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	item.Title = "Updated title"
	item.Notes = "Updated notes"
	if err := s.UpdateItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetItem(ctx, "01A")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Updated title" {
		t.Errorf("title = %q, want %q", got.Title, "Updated title")
	}
}

func TestDeleteItem(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.CreateItem(ctx, testItem("01A", model.TypeSnippet)); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteItem(ctx, "01A"); err != nil {
		t.Fatal(err)
	}

	_, err := s.GetItem(ctx, "01A")
	if err == nil {
		t.Error("expected error for deleted item")
	}
}

func TestTags(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	item := testItem("01A", model.TypeSnippet)
	item.Tags = []model.Tag{{Name: "alpha"}}
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	// Add tag
	if err := s.AddTag(ctx, "01A", "beta"); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetItem(ctx, "01A")
	if len(got.Tags) != 2 {
		t.Errorf("tags = %d, want 2", len(got.Tags))
	}

	// Rename tag
	if err := s.RenameTag(ctx, "alpha", "gamma"); err != nil {
		t.Fatal(err)
	}

	tags, _ := s.ListTags(ctx)
	found := false
	for _, tg := range tags {
		if tg.Name == "gamma" {
			found = true
		}
	}
	if !found {
		t.Error("renamed tag 'gamma' not found")
	}

	// Remove tag
	if err := s.RemoveTag(ctx, "01A", "beta"); err != nil {
		t.Fatal(err)
	}

	got, _ = s.GetItem(ctx, "01A")
	if len(got.Tags) != 1 {
		t.Errorf("tags after remove = %d, want 1", len(got.Tags))
	}
}

func TestCollections(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	col, err := s.CreateCollection(ctx, "reading", "Things to read")
	if err != nil {
		t.Fatal(err)
	}
	if col.Name != "reading" {
		t.Errorf("name = %q, want %q", col.Name, "reading")
	}

	item := testItem("01A", model.TypeLink)
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToCollection(ctx, "01A", "reading"); err != nil {
		t.Fatal(err)
	}

	items, err := s.ListCollectionItems(ctx, "reading", model.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("collection items = %d, want 1", len(items))
	}

	// Delete collection
	if err := s.DeleteCollection(ctx, "reading"); err != nil {
		t.Fatal(err)
	}
	cols, _ := s.ListCollections(ctx)
	if len(cols) != 0 {
		t.Errorf("collections after delete = %d, want 0", len(cols))
	}
}

func TestListItemsFilterByTag(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	item1 := testItem("01A", model.TypeSnippet)
	item1.Tags = []model.Tag{{Name: "go"}}
	if err := s.CreateItem(ctx, item1); err != nil {
		t.Fatal(err)
	}

	item2 := testItem("01B", model.TypeSnippet)
	item2.Tags = []model.Tag{{Name: "python"}}
	if err := s.CreateItem(ctx, item2); err != nil {
		t.Fatal(err)
	}

	items, err := s.ListItems(ctx, model.ItemFilter{Tags: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("got %d items, want 1", len(items))
	}
}
