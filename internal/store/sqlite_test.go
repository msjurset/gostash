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
		item := testItem(id, model.TypeURL)
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

	if err := s.CreateItem(ctx, testItem("01A", model.TypeURL)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateItem(ctx, testItem("01B", model.TypeSnippet)); err != nil {
		t.Fatal(err)
	}

	items, err := s.ListItems(ctx, model.ItemFilter{Type: model.TypeURL})
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

func TestArchiveItem(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.CreateItem(ctx, testItem("01A", model.TypeSnippet)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateItem(ctx, testItem("01B", model.TypeSnippet)); err != nil {
		t.Fatal(err)
	}

	// New items default to unarchived.
	got, err := s.GetItem(ctx, "01A")
	if err != nil {
		t.Fatal(err)
	}
	if got.Archived {
		t.Errorf("new item should not be archived")
	}

	// Archive 01A.
	if err := s.SetArchived(ctx, "01A", true); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetItem(ctx, "01A")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Archived {
		t.Errorf("expected archived=true")
	}

	// Default list excludes archived items.
	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "01B" {
		t.Errorf("default list should exclude archived; got %+v", idsOf(items))
	}

	// IncludeArchived widens to both.
	items, err = s.ListItems(ctx, model.ItemFilter{Limit: 100, IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("include-archived should return both; got %v", idsOf(items))
	}

	// OnlyArchived narrows to just archived.
	items, err = s.ListItems(ctx, model.ItemFilter{Limit: 100, OnlyArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "01A" {
		t.Errorf("only-archived should return just 01A; got %v", idsOf(items))
	}

	// Unarchive restores it.
	if err := s.SetArchived(ctx, "01A", false); err != nil {
		t.Fatal(err)
	}
	items, err = s.ListItems(ctx, model.ItemFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("after unarchive both should be visible; got %v", idsOf(items))
	}

	// Missing ID errors out.
	if err := s.SetArchived(ctx, "nonexistent-id", true); err == nil {
		t.Errorf("expected error for unknown id")
	}
}

func TestThumbnailPathRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	item := testItem("01THUMB", model.TypeFile)
	item.ThumbnailPath = "thumbnails/01THUMB.jpg"
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetItem(ctx, "01THUMB")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ThumbnailPath != "thumbnails/01THUMB.jpg" {
		t.Errorf("thumbnail_path round-trip: got %q, want %q",
			got.ThumbnailPath, "thumbnails/01THUMB.jpg")
	}

	got.ThumbnailPath = "thumbnails/01THUMB.png"
	if err := s.UpdateItem(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, err := s.GetItem(ctx, "01THUMB")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got2.ThumbnailPath != "thumbnails/01THUMB.png" {
		t.Errorf("after update: got %q, want %q",
			got2.ThumbnailPath, "thumbnails/01THUMB.png")
	}
}

func TestSavedSearchLiveRoundTrip(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.SaveSearch(ctx, "static-one", "foo", model.ItemFilter{Type: model.TypeURL}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSearch(ctx, "smart-one", "bar", model.ItemFilter{Tags: []string{"work"}}, true); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListSavedSearches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	byName := map[string]model.SavedSearch{}
	for _, ss := range got {
		byName[ss.Name] = ss
	}
	if byName["static-one"].Live {
		t.Errorf("static-one should not be live")
	}
	if !byName["smart-one"].Live {
		t.Errorf("smart-one should be live")
	}

	// Round-trip via GetSavedSearch.
	smart, err := s.GetSavedSearch(ctx, "smart-one")
	if err != nil {
		t.Fatal(err)
	}
	if !smart.Live {
		t.Errorf("smart-one round-trip lost live=true")
	}

	// Re-saving the same name updates the live flag.
	if err := s.SaveSearch(ctx, "smart-one", "bar", model.ItemFilter{}, false); err != nil {
		t.Fatal(err)
	}
	smart, err = s.GetSavedSearch(ctx, "smart-one")
	if err != nil {
		t.Fatal(err)
	}
	if smart.Live {
		t.Errorf("re-save should have flipped live to false")
	}
}

func TestListItemsExcludeTags(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	cases := []struct {
		id   string
		tags []string
	}{
		{"01A", []string{"alpha"}},
		{"01B", []string{"alpha", "beta"}},
		{"01C", []string{"gamma"}},
		{"01D", nil},
	}
	for _, c := range cases {
		item := testItem(c.id, model.TypeURL)
		for _, t := range c.tags {
			item.Tags = append(item.Tags, model.Tag{Name: t})
		}
		if err := s.CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	// Exclude beta — keeps 01A, 01C, 01D.
	items, err := s.ListItems(ctx, model.ItemFilter{
		Limit:       100,
		ExcludeTags: []string{"beta"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := idsOf(items)
	wantSet := map[string]bool{"01A": true, "01C": true, "01D": true}
	if len(got) != 3 {
		t.Fatalf("got %v, want 3 ids", got)
	}
	for _, id := range got {
		if !wantSet[id] {
			t.Errorf("unexpected id %q", id)
		}
	}

	// Compose include + exclude: tag alpha AND NOT beta → just 01A.
	items, err = s.ListItems(ctx, model.ItemFilter{
		Limit:       100,
		Tags:        []string{"alpha"},
		ExcludeTags: []string{"beta"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(items); len(got) != 1 || got[0] != "01A" {
		t.Errorf("compose include+exclude got %v, want [01A]", got)
	}
}

func TestListItemsUntagged(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	tagged := testItem("01A", model.TypeURL)
	tagged.Tags = []model.Tag{{Name: "alpha"}}
	if err := s.CreateItem(ctx, tagged); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateItem(ctx, testItem("01B", model.TypeURL)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateItem(ctx, testItem("01C", model.TypeURL)); err != nil {
		t.Fatal(err)
	}

	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 100, Untagged: true})
	if err != nil {
		t.Fatal(err)
	}
	got := idsOf(items)
	if len(got) != 2 {
		t.Fatalf("untagged got %v, want 2", got)
	}
	for _, id := range got {
		if id == "01A" {
			t.Errorf("tagged item 01A should be excluded")
		}
	}

	// Untagged short-circuits include-tags.
	items, err = s.ListItems(ctx, model.ItemFilter{
		Limit:    100,
		Untagged: true,
		Tags:     []string{"alpha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(items); len(got) != 2 {
		t.Errorf("untagged should ignore include-tags; got %v", got)
	}
}

func TestListItemsRecent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	old := testItem("01A", model.TypeURL)
	old.CreatedAt = now.Add(-30 * 24 * time.Hour)
	if err := s.CreateItem(ctx, old); err != nil {
		t.Fatal(err)
	}
	recent := testItem("01B", model.TypeURL)
	recent.CreatedAt = now.Add(-1 * time.Hour)
	if err := s.CreateItem(ctx, recent); err != nil {
		t.Fatal(err)
	}

	// "7d" should match the 1-hour-old item but exclude the 30-day-old one.
	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 100, Recent: "7d"})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(items); len(got) != 1 || got[0] != "01B" {
		t.Errorf("recent=7d got %v, want [01B]", got)
	}

	// "1w" matches the same window via the week shorthand.
	items, err = s.ListItems(ctx, model.ItemFilter{Limit: 100, Recent: "1w"})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(items); len(got) != 1 || got[0] != "01B" {
		t.Errorf("recent=1w got %v, want [01B]", got)
	}

	// Unparseable specs are ignored, not errored.
	items, err = s.ListItems(ctx, model.ItemFilter{Limit: 100, Recent: "garbage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("unparseable recent should be ignored; got %d", len(items))
	}
}

func TestListItemsRegex(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	mk := func(id, title, url string) *model.Item {
		it := testItem(id, model.TypeURL)
		it.Title = title
		it.URL = url
		return it
	}
	for _, it := range []*model.Item{
		mk("01A", "Go regex tutorial", "https://golang.org/pkg/regexp/"),
		mk("01B", "Python regex tutorial", "https://docs.python.org/3/library/re.html"),
		mk("01C", "Knitting basics", "https://example.com/knit"),
	} {
		if err := s.CreateItem(ctx, it); err != nil {
			t.Fatal(err)
		}
	}

	// Anchor: titles starting with "Go".
	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 100, Regex: "^Go "})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(items); len(got) != 1 || got[0] != "01A" {
		t.Errorf("regex `^Go ` got %v, want [01A]", got)
	}

	// Negation: not containing "regex" anywhere → just 01C.
	items, err = s.ListItems(ctx, model.ItemFilter{Limit: 100, Regex: "!regex"})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(items); len(got) != 1 || got[0] != "01C" {
		t.Errorf("regex `!regex` got %v, want [01C]", got)
	}

	// Compose with another filter (URL pattern + tag would shrink further;
	// here just verify regex is matched against URL too).
	items, err = s.ListItems(ctx, model.ItemFilter{
		Limit: 100,
		Regex: `python\.org`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(items); len(got) != 1 || got[0] != "01B" {
		t.Errorf("regex against url got %v, want [01B]", got)
	}

	// Invalid regex is treated as no-op (returns all 3 instead of an error).
	items, err = s.ListItems(ctx, model.ItemFilter{Limit: 100, Regex: "[unterminated"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Errorf("invalid regex should be a no-op; got %d", len(items))
	}
}

// Regression: `stash search --regex pattern -l N` was returning empty
// because the SQL LIMIT was applied BEFORE the regex filter — the top
// N newest rows were fetched and most didn't match. Filter must apply
// to the full candidate set with truncation done post-regex.
func TestListItemsRegexLimitPostFilter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// 10 items, only 2 of them ("github" in title) should match. The
	// matching items are the OLDEST so they'd never appear in the
	// top-3 by created_at if the limit applied pre-regex.
	now := time.Now().UTC()
	mk := func(id, title string, ageMinutes int) *model.Item {
		it := testItem(id, model.TypeURL)
		it.Title = title
		it.URL = "https://example.com/" + id
		it.CreatedAt = now.Add(time.Duration(-ageMinutes) * time.Minute)
		it.UpdatedAt = it.CreatedAt
		return it
	}
	rows := []*model.Item{
		// Newest first by created_at descending — the SQL LIMIT
		// without our fix would have returned the first 3 of these.
		mk("01N1", "newest 1", 1),
		mk("01N2", "newest 2", 2),
		mk("01N3", "newest 3", 3),
		mk("01N4", "newest 4", 4),
		mk("01N5", "newest 5", 5),
		mk("01N6", "newest 6", 6),
		mk("01N7", "newest 7", 7),
		mk("01N8", "newest 8", 8),
		// Both matches sit at the OLDEST end. Without the fix they
		// would have been excluded by an early SQL LIMIT 3.
		mk("01M1", "github older A", 100),
		mk("01M2", "github older B", 200),
	}
	for _, it := range rows {
		if err := s.CreateItem(ctx, it); err != nil {
			t.Fatal(err)
		}
	}

	items, err := s.ListItems(ctx, model.ItemFilter{Limit: 3, Regex: "github"})
	if err != nil {
		t.Fatal(err)
	}
	if got := idsOf(items); len(got) != 2 || !contains(got, "01M1") || !contains(got, "01M2") {
		t.Errorf("regex+limit got %v, want both matching ids regardless of limit", got)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func idsOf(items []model.Item) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
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

	item := testItem("01A", model.TypeURL)
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

func TestCreateItemRoundTripsLocation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	item := testItem("01LOC", model.TypeImage)
	item.Location = &model.Location{Lat: 33.7544777, Lon: -84.6272805, Source: "exif"}
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := s.GetItem(ctx, "01LOC")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Location == nil {
		t.Fatalf("Location = nil, want non-nil")
	}
	if got.Location.Lat != 33.7544777 || got.Location.Lon != -84.6272805 {
		t.Errorf("Location = %+v, want lat=33.7544777 lon=-84.6272805", got.Location)
	}
	if got.Location.Source != "exif" {
		t.Errorf("Location.Source = %q, want %q", got.Location.Source, "exif")
	}
}

func TestUpdateItemClearsLocation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	item := testItem("01CLR", model.TypeImage)
	item.Location = &model.Location{Lat: 1, Lon: 2, Source: "manual"}
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}

	item.Location = nil
	if err := s.UpdateItem(ctx, item); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.GetItem(ctx, "01CLR")
	if err != nil {
		t.Fatal(err)
	}
	if got.Location != nil {
		t.Errorf("Location = %+v, want nil after clear", got.Location)
	}
}

func TestItemWithoutLocationStaysNil(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	item := testItem("01NIL", model.TypeImage)
	if err := s.CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetItem(ctx, "01NIL")
	if err != nil {
		t.Fatal(err)
	}
	if got.Location != nil {
		t.Errorf("Location = %+v, want nil for image without GPS", got.Location)
	}
}
