package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/msjurset/gostash/internal/model"
)

// RelatedItems ranks every other item by overlap with the given
// source item and returns the top N. Scoring signals (additive):
//
//	+3 per shared tag
//	+2 per shared collection
//	+4 per existing manual link (either direction)
//	+5 if content_hash matches exactly (true duplicates)
//	+2 if the URL host (sans `www.`) matches the source
//	+6 * cosine_similarity (up to +6 conceptual boost)
//
// Archived items are excluded; the source item itself is excluded.
// Ties are broken by recency (updated_at DESC).
func (s *SQLiteStore) RelatedItems(ctx context.Context, source *model.Item, limit int) ([]model.Item, error) {
	if source == nil || source.ID == "" {
		return nil, fmt.Errorf("source item required")
	}
	if limit <= 0 {
		limit = 5
	}

	// 1. Get heuristic candidates from SQL (tags, collections, links, hash)
	candidatePool := limit * 4
	if candidatePool < 40 {
		candidatePool = 40
	}
	scored, err := s.relatedScored(ctx, source, candidatePool)
	if err != nil {
		return nil, err
	}

	// 2. Conceptual similarity (Vector)
	_, sourceVector, err := s.GetItemEmbedding(ctx, source.ID)
	if err == nil && len(sourceVector) > 0 {
		// We have a source vector. Fetch ALL embeddings to find top
		// conceptual matches that might not have shared tags/etc.
		semanticResults, serr := s.SearchSemantic(ctx, sourceVector, model.ItemFilter{
			Limit:           candidatePool,
			IncludeArchived: false,
		})
		if serr == nil {
			// Merge semantic results into our scored list.
			// Map for easy lookup/update.
			idMap := make(map[string]int)
			for i, sc := range scored {
				idMap[sc.item.ID] = i
			}

			for i, sem := range semanticResults {
				if sem.ID == source.ID {
					continue
				}
				// Boost based on rank in semantic results (up to +6 pts)
				boost := 6.0 * (1.0 / float64(i+1))
				if idx, ok := idMap[sem.ID]; ok {
					scored[idx].score += int(boost)
				} else {
					// conceptual match not already in heuristic pool
					scored = append(scored, scoredItem{item: sem, score: int(boost)})
				}
			}
		}
	}

	// 3. Domain bump in Go.
	if sourceHost := normalizedHost(source.URL); sourceHost != "" {
		for i := range scored {
			if normalizedHost(scored[i].item.URL) == sourceHost {
				scored[i].score += 2
			}
		}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].item.UpdatedAt.After(scored[j].item.UpdatedAt)
	})

	if len(scored) > limit {
		scored = scored[:limit]
	}
	out := make([]model.Item, 0, len(scored))
	for _, s := range scored {
		out = append(out, s.item)
	}
	return out, nil
}

type scoredItem struct {
	item  model.Item
	score int
}

// relatedScored runs the SQL scoring pass: UNION ALL across the four
// signal sources, SUM per item, JOIN back to items, ORDER by score.
// Returns the top `limit` (caller may re-rank with the domain bump).
func (s *SQLiteStore) relatedScored(ctx context.Context, source *model.Item, limit int) ([]scoredItem, error) {
	q := `
WITH scored AS (
    SELECT it.item_id AS id, 3 AS pts
        FROM item_tags it
        WHERE it.tag_id IN (SELECT tag_id FROM item_tags WHERE item_id = ?)
          AND it.item_id != ?
    UNION ALL
    SELECT ic.item_id AS id, 2 AS pts
        FROM item_collections ic
        WHERE ic.collection_id IN (SELECT collection_id FROM item_collections WHERE item_id = ?)
          AND ic.item_id != ?
    UNION ALL
    SELECT il.item_id_to AS id, 4 AS pts
        FROM item_links il
        WHERE il.item_id_from = ?
    UNION ALL
    SELECT il.item_id_from AS id, 4 AS pts
        FROM item_links il
        WHERE il.item_id_to = ?
    UNION ALL
    SELECT i.id, 5 AS pts
        FROM items i
        WHERE i.content_hash = ?
          AND i.content_hash != ''
          AND i.id != ?
)
SELECT i.id, i.type, i.title, i.url, i.notes,
       i.source_path, i.store_path, i.content_hash, i.extracted_text,
       i.mime_type, i.file_size, i.metadata, i.created_at, i.updated_at,
       i.archived, i.thumbnail_path,
       i.latitude, i.longitude, i.location_source, i.captured_at, i.chat_history,
       SUM(s.pts) AS score
FROM items i
JOIN scored s ON s.id = i.id
WHERE i.archived = 0
GROUP BY i.id
ORDER BY score DESC, i.updated_at DESC
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q,
		source.ID, source.ID, // shared tags
		source.ID, source.ID, // shared collections
		source.ID,            // links from
		source.ID,            // links to
		source.ContentHash, source.ID, // content hash
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("related score query: %w", err)
	}
	defer rows.Close()

	var out []scoredItem
	for rows.Next() {
		item, score, err := s.scanRelatedRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, scoredItem{item: *item, score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		if err := s.loadRelations(ctx, &out[i].item); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *SQLiteStore) scanRelatedRow(rows interface {
	Scan(dest ...any) error
}) (*model.Item, int, error) {
	var item model.Item
	var meta string
	var archived int
	var score int
	var lat, lon sql.NullFloat64
	var locSrc sql.NullString
	var capturedAt sql.NullTime
	var chatHistoryStr string

	err := rows.Scan(
		&item.ID, &item.Type, &item.Title, &item.URL, &item.Notes,
		&item.SourcePath, &item.StorePath, &item.ContentHash, &item.ExtractedText,
		&item.MimeType, &item.FileSize, &meta, &item.CreatedAt, &item.UpdatedAt,
		&archived, &item.ThumbnailPath,
		&lat, &lon, &locSrc, &capturedAt, &chatHistoryStr,
		&score,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("scan related: %w", err)
	}
	item.Metadata = json.RawMessage(meta)
	item.Archived = archived != 0
	item.Location = buildLocation(lat, lon, locSrc)
	item.CapturedAt = nullTimeToPtr(capturedAt)
	if err := json.Unmarshal([]byte(chatHistoryStr), &item.ChatHistory); err != nil {
		item.ChatHistory = nil
	}
	return &item, score, nil
}
func normalizedHost(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.")
}
