package main

import (
	"testing"
	"time"

	"github.com/msjurset/gostash/internal/model"
)

func momentTestItem(id string, t time.Time, tags ...string) model.Item {
	it := model.Item{ID: id, CreatedAt: t}
	for _, name := range tags {
		it.Tags = append(it.Tags, model.Tag{Name: name})
	}
	return it
}

func TestClusterByTime(t *testing.T) {
	base := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		items   []model.Item
		gap     time.Duration
		want    [][]string // cluster IDs
	}{
		{
			name:  "empty input",
			items: nil,
			gap:   1 * time.Hour,
			want:  nil,
		},
		{
			name: "single item is one cluster",
			items: []model.Item{
				momentTestItem("a", base),
			},
			gap:  1 * time.Hour,
			want: [][]string{{"a"}},
		},
		{
			name: "two within gap cluster together",
			items: []model.Item{
				momentTestItem("a", base),
				momentTestItem("b", base.Add(30*time.Minute)),
			},
			gap:  1 * time.Hour,
			want: [][]string{{"a", "b"}},
		},
		{
			name: "two beyond gap split",
			items: []model.Item{
				momentTestItem("a", base),
				momentTestItem("b", base.Add(3*time.Hour)),
			},
			gap:  1 * time.Hour,
			want: [][]string{{"a"}, {"b"}},
		},
		{
			name: "mixed: tight burst then long pause then pair",
			items: []model.Item{
				momentTestItem("a", base),
				momentTestItem("b", base.Add(15*time.Minute)),
				momentTestItem("c", base.Add(45*time.Minute)),
				momentTestItem("d", base.Add(8*time.Hour)),
				momentTestItem("e", base.Add(9*time.Hour)),
			},
			gap:  2 * time.Hour,
			want: [][]string{{"a", "b", "c"}, {"d", "e"}},
		},
		{
			name: "out-of-order input gets sorted",
			items: []model.Item{
				momentTestItem("c", base.Add(45*time.Minute)),
				momentTestItem("a", base),
				momentTestItem("b", base.Add(15*time.Minute)),
			},
			gap:  2 * time.Hour,
			want: [][]string{{"a", "b", "c"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clusterByTime(tc.items, tc.gap)
			if len(got) != len(tc.want) {
				t.Fatalf("clusters: got %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for i, cluster := range got {
				if len(cluster) != len(tc.want[i]) {
					t.Errorf("cluster[%d]: got %d items, want %d", i, len(cluster), len(tc.want[i]))
					continue
				}
				for j, item := range cluster {
					if item.ID != tc.want[i][j] {
						t.Errorf("cluster[%d][%d]: got %q, want %q", i, j, item.ID, tc.want[i][j])
					}
				}
			}
		})
	}
}

func TestComputeSharedTags(t *testing.T) {
	cases := []struct {
		name  string
		items []model.Item
		want  []string
	}{
		{
			name:  "empty cluster",
			items: nil,
			want:  nil,
		},
		{
			name: "tag in every item ⇒ shared",
			items: []model.Item{
				momentTestItem("a", time.Now(), "beach", "nature"),
				momentTestItem("b", time.Now(), "beach"),
				momentTestItem("c", time.Now(), "beach", "sunset"),
			},
			want: []string{"beach"},
		},
		{
			name: "tag below 75% threshold dropped",
			items: []model.Item{
				momentTestItem("a", time.Now(), "beach", "nature"),
				momentTestItem("b", time.Now(), "beach"),
				momentTestItem("c", time.Now(), "sunset"),
				momentTestItem("d", time.Now(), "sunset"),
			},
			want: nil, // beach only appears in 2/4 = 50%, nature 1/4
		},
		{
			name: "exactly 75% threshold passes",
			items: []model.Item{
				momentTestItem("a", time.Now(), "trip"),
				momentTestItem("b", time.Now(), "trip"),
				momentTestItem("c", time.Now(), "trip"),
				momentTestItem("d", time.Now()),
			},
			want: []string{"trip"}, // 3/4 = 75%
		},
		{
			name: "duplicate tags on one item count as one",
			items: []model.Item{
				{ID: "a", Tags: []model.Tag{{Name: "x"}, {Name: "x"}}},
				{ID: "b", Tags: []model.Tag{{Name: "x"}}},
			},
			want: []string{"x"},
		},
		{
			name: "all-distinct cluster of 2 returns nothing",
			items: []model.Item{
				momentTestItem("a", time.Now(), "p"),
				momentTestItem("b", time.Now(), "q"),
			},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeSharedTags(tc.items)
			if !equalStrSlices(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestComputeLocationCenter(t *testing.T) {
	loc := func(lat, lon float64) *model.Location {
		return &model.Location{Lat: lat, Lon: lon}
	}

	items := []model.Item{
		{ID: "a", Location: loc(33.0, -79.0)},
		{ID: "b", Location: nil},
		{ID: "c", Location: loc(33.2, -79.2)},
	}
	got, count := computeLocationCenter(items)
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}
	const eps = 1e-9
	wantLat := (33.0 + 33.2) / 2
	wantLon := (-79.0 + -79.2) / 2
	if got.Lat < wantLat-eps || got.Lat > wantLat+eps {
		t.Errorf("lat: got %f, want %f", got.Lat, wantLat)
	}
	if got.Lon < wantLon-eps || got.Lon > wantLon+eps {
		t.Errorf("lon: got %f, want %f", got.Lon, wantLon)
	}

	// All-nil cluster ⇒ count 0, no signal.
	if _, count := computeLocationCenter([]model.Item{{ID: "x"}, {ID: "y"}}); count != 0 {
		t.Errorf("nil-only count: got %d, want 0", count)
	}
}

func TestAllInSameCollection(t *testing.T) {
	col := func(names ...string) []model.Collection {
		var out []model.Collection
		for _, n := range names {
			out = append(out, model.Collection{Name: n})
		}
		return out
	}

	// All share collection "trip-1" → true.
	if !allInSameCollection([]model.Item{
		{ID: "a", Collections: col("trip-1")},
		{ID: "b", Collections: col("trip-1", "favs")},
		{ID: "c", Collections: col("trip-1")},
	}) {
		t.Error("expected true when all items share 'trip-1'")
	}

	// One item is uncollected → false (we don't suppress).
	if allInSameCollection([]model.Item{
		{ID: "a", Collections: col("trip-1")},
		{ID: "b", Collections: nil},
	}) {
		t.Error("expected false when one item is uncollected")
	}

	// No overlap → false.
	if allInSameCollection([]model.Item{
		{ID: "a", Collections: col("trip-1")},
		{ID: "b", Collections: col("trip-2")},
	}) {
		t.Error("expected false when collections don't overlap")
	}

	// Empty cluster guard.
	if allInSameCollection(nil) {
		t.Error("expected false for nil cluster")
	}
}

func TestNameFor(t *testing.T) {
	mk := func(start, end string, tags ...string) MomentSuggestion {
		s, _ := time.Parse(time.RFC3339, start)
		e, _ := time.Parse(time.RFC3339, end)
		return MomentSuggestion{Start: s, End: e, SharedTags: tags}
	}
	cases := []struct {
		name string
		sug  MomentSuggestion
		want string
	}{
		{
			name: "same-day, no tags",
			sug:  mk("2026-05-15T09:00:00Z", "2026-05-15T18:00:00Z"),
			want: "2026-05-15",
		},
		{
			name: "same-month range collapses end to day",
			sug:  mk("2026-05-15T09:00:00Z", "2026-05-17T18:00:00Z"),
			want: "2026-05-15 → 17",
		},
		{
			name: "cross-month uses full dates",
			sug:  mk("2026-05-30T09:00:00Z", "2026-06-02T18:00:00Z"),
			want: "2026-05-30 → 2026-06-02",
		},
		{
			name: "shared tag becomes the qualifier",
			sug:  mk("2026-05-15T09:00:00Z", "2026-05-15T18:00:00Z", "beach", "nature"),
			want: "beach — 2026-05-15",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nameFor(tc.sug); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildSuggestions_EndToEnd(t *testing.T) {
	base := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	loc := func(lat, lon float64) *model.Location {
		return &model.Location{Lat: lat, Lon: lon}
	}
	items := []model.Item{
		// Trip cluster: 5 items same day, shared "beach", with GPS
		{ID: "1", CreatedAt: base, Tags: []model.Tag{{Name: "beach"}}, Location: loc(33.0, -79.0)},
		{ID: "2", CreatedAt: base.Add(30 * time.Minute), Tags: []model.Tag{{Name: "beach"}}, Location: loc(33.01, -79.01)},
		{ID: "3", CreatedAt: base.Add(2 * time.Hour), Tags: []model.Tag{{Name: "beach"}}, Location: loc(33.02, -79.02)},
		{ID: "4", CreatedAt: base.Add(3 * time.Hour), Tags: []model.Tag{{Name: "beach"}}},
		{ID: "5", CreatedAt: base.Add(5 * time.Hour), Tags: []model.Tag{{Name: "beach"}}},

		// Lone unrelated item far away
		{ID: "lone", CreatedAt: base.Add(48 * time.Hour)},

		// Pair, too small to surface (min 3)
		{ID: "p1", CreatedAt: base.Add(72 * time.Hour)},
		{ID: "p2", CreatedAt: base.Add(73 * time.Hour)},
	}

	got := buildSuggestions(items, momentParams{
		MaxGap:   6 * time.Hour,
		MaxSpan:  5 * 24 * time.Hour,
		MinItems: 3,
	})
	if len(got) != 1 {
		t.Fatalf("expected 1 suggestion, got %d: %+v", len(got), got)
	}
	sug := got[0]
	if sug.ItemCount != 5 {
		t.Errorf("ItemCount: got %d, want 5", sug.ItemCount)
	}
	if !equalStrSlices(sug.SharedTags, []string{"beach"}) {
		t.Errorf("SharedTags: got %v, want [beach]", sug.SharedTags)
	}
	if sug.LocationCount != 3 {
		t.Errorf("LocationCount: got %d, want 3", sug.LocationCount)
	}
	if sug.SuggestedName != "beach — 2026-05-15" {
		t.Errorf("name: got %q", sug.SuggestedName)
	}
	// Score: 5 (size) + 2*1 (one shared tag) + 3 + 3*0.5 (3 located items) = 11.5
	if sug.Score < 11 || sug.Score > 12 {
		t.Errorf("Score: got %f, want ~11.5", sug.Score)
	}
}

func TestMomentSignatureIsOrderIndependent(t *testing.T) {
	a := []MomentItemPreview{{ID: "x"}, {ID: "y"}, {ID: "z"}}
	b := []MomentItemPreview{{ID: "z"}, {ID: "x"}, {ID: "y"}}
	c := []MomentItemPreview{{ID: "x"}, {ID: "y"}}
	if momentSignature(a) != momentSignature(b) {
		t.Error("signature should be order-independent")
	}
	if momentSignature(a) == momentSignature(c) {
		t.Error("removing an item must change the signature")
	}
}

func TestBuildSuggestions_RespectsDismissedSignatures(t *testing.T) {
	base := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	items := []model.Item{
		{ID: "1", CreatedAt: base},
		{ID: "2", CreatedAt: base.Add(time.Hour)},
		{ID: "3", CreatedAt: base.Add(2 * time.Hour)},
	}
	// First build with no dismissals — should surface the cluster.
	got := buildSuggestions(items, momentParams{
		MaxGap:   6 * time.Hour,
		MaxSpan:  5 * 24 * time.Hour,
		MinItems: 3,
	})
	if len(got) != 1 {
		t.Fatalf("baseline: expected 1 suggestion, got %d", len(got))
	}
	sig := got[0].Signature

	// Now dismiss it — the same input should produce zero output.
	got = buildSuggestions(items, momentParams{
		MaxGap:              6 * time.Hour,
		MaxSpan:             5 * 24 * time.Hour,
		MinItems:            3,
		DismissedSignatures: map[string]bool{sig: true},
	})
	if len(got) != 0 {
		t.Errorf("dismissed cluster should be filtered, got %d", len(got))
	}

	// IncludeDismissed flips the filter back off.
	got = buildSuggestions(items, momentParams{
		MaxGap:              6 * time.Hour,
		MaxSpan:             5 * 24 * time.Hour,
		MinItems:            3,
		DismissedSignatures: map[string]bool{sig: true},
		IncludeDismissed:    true,
	})
	if len(got) != 1 {
		t.Errorf("--include-dismissed should re-surface, got %d", len(got))
	}
}

func TestBuildSuggestions_DropsAlreadyCollected(t *testing.T) {
	base := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	col := []model.Collection{{Name: "Pawleys-2026-05"}}
	items := []model.Item{
		{ID: "1", CreatedAt: base, Collections: col},
		{ID: "2", CreatedAt: base.Add(time.Hour), Collections: col},
		{ID: "3", CreatedAt: base.Add(2 * time.Hour), Collections: col},
	}
	got := buildSuggestions(items, momentParams{
		MaxGap:   6 * time.Hour,
		MaxSpan:  5 * 24 * time.Hour,
		MinItems: 3,
	})
	if len(got) != 0 {
		t.Errorf("expected 0 suggestions (cluster already collected), got %d", len(got))
	}
}

func TestBuildSuggestions_DropsOverlongSpan(t *testing.T) {
	base := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	// 4 items within MaxGap but spanning 7 days total — beyond MaxSpan.
	items := []model.Item{
		{ID: "1", CreatedAt: base},
		{ID: "2", CreatedAt: base.Add(5 * time.Hour)},
		{ID: "3", CreatedAt: base.Add(5*24*time.Hour + 5*time.Hour)},
		{ID: "4", CreatedAt: base.Add(7 * 24 * time.Hour)},
	}
	got := buildSuggestions(items, momentParams{
		MaxGap:   6 * time.Hour,
		MaxSpan:  5 * 24 * time.Hour,
		MinItems: 3,
	})
	// The 4 items are NOT clustered because of the 5-day gap mid-list
	// (which exceeds MaxGap). So they form three clusters of size
	// 2/1/1, none of which meet min-items. Tests the early-exit path.
	if len(got) != 0 {
		t.Errorf("expected 0 suggestions, got %d", len(got))
	}
}
