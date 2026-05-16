package store

import (
	"context"
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
//
// Archived items are excluded; the source item itself is excluded.
// Ties are broken by recency (updated_at DESC).
//
// The tag/link/collection/hash scoring runs in one SQL query for
// speed; the domain bump is applied client-side since SQL has no
// portable URL parser and string-matching on `url LIKE '%domain%'`
// produces false positives (e.g. `foo.example.com` matching `example.com`
// substrings on unrelated pages).
func (s *SQLiteStore) RelatedItems(ctx context.Context, source *model.Item, limit int) ([]model.Item, error) {
	if source == nil || source.ID == "" {
		return nil, fmt.Errorf("source item required")
	}
	if limit <= 0 {
		limit = 5
	}

	// Over-fetch from the SQL pass so the domain bump in Go has a
	// candidate pool to re-rank against. 4× the requested limit
	// gives the boost room to lift a same-domain item that
	// originally scored just below the cutoff.
	candidatePool := limit * 4
	if candidatePool < 20 {
		candidatePool = 20
	}

	scored, err := s.relatedScored(ctx, source, candidatePool)
	if err != nil {
		return nil, err
	}

	// Domain bump in Go. Cheap: parse the source URL once, compare
	// against each candidate's URL host.
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
	// Drain the cursor BEFORE loading per-item relations. SQLite via
	// database/sql can stall when a second query is issued on the
	// shared pool while the parent cursor is still open — popular-tag
	// items would just sit on a hung loader spinner indefinitely.
	var out []scoredItem
	for rows.Next() {
		item, score, err := s.scanRelatedRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, scoredItem{item: *item, score: score})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for i := range out {
		if err := s.loadRelations(ctx, &out[i].item); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// scanRelatedRow mirrors `scanItems`' column list with the extra
// `score` trailing column from the GROUP BY. Kept inline rather than
// generalizing scanItems because the score isn't part of model.Item
// and only this caller wants it.
func (s *SQLiteStore) scanRelatedRow(rows interface {
	Scan(dest ...any) error
}) (*model.Item, int, error) {
	var item model.Item
	var meta string
	var archived int
	var score int
	err := rows.Scan(
		&item.ID, &item.Type, &item.Title, &item.URL, &item.Notes,
		&item.SourcePath, &item.StorePath, &item.ContentHash, &item.ExtractedText,
		&item.MimeType, &item.FileSize, &meta, &item.CreatedAt, &item.UpdatedAt,
		&archived, &item.ThumbnailPath,
		&score,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("scan related: %w", err)
	}
	item.Metadata = []byte(meta)
	item.Archived = archived != 0
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
