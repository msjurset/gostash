package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/model"
)

// ───────────────────────────────────────────────────────────
// Feed sources
// ───────────────────────────────────────────────────────────

func (s *SQLiteStore) CreateFeedSource(ctx context.Context, src *model.FeedSource) error {
	if src.Name == "" || src.URL == "" || src.Kind == "" {
		return fmt.Errorf("name, url, and kind are required")
	}
	if src.PollIntervalMinutes <= 0 {
		src.PollIntervalMinutes = 360
	}
	tagsJSON, _ := json.Marshal(src.DefaultTags)
	if len(tagsJSON) == 0 {
		tagsJSON = []byte("[]")
	}
	now := time.Now().UTC()
	src.CreatedAt = now
	src.UpdatedAt = now
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO feed_sources(name, kind, url, default_tags, default_collection, auto_stash, fetch_content, poll_interval_minutes, enabled, last_polled_at, last_error, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		src.Name, src.Kind, src.URL,
		string(tagsJSON), src.DefaultCollection,
		boolToInt(src.AutoStash), boolToInt(src.FetchContent),
		src.PollIntervalMinutes, boolToInt(src.Enabled),
		nullableUnix(src.LastPolledAt), src.LastError,
		now.Unix(), now.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert feed_source: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	src.ID = id
	return nil
}

// GetFeedSource accepts a numeric id or a unique name. Lets the CLI
// be friendly without forcing the user to look up id values.
func (s *SQLiteStore) GetFeedSource(ctx context.Context, idOrName string) (*model.FeedSource, error) {
	var row *sql.Row
	if id, err := strconv.ParseInt(idOrName, 10, 64); err == nil {
		row = s.db.QueryRowContext(ctx, feedSourceSelectByID, id)
	} else {
		row = s.db.QueryRowContext(ctx, feedSourceSelectByName, idOrName)
	}
	return scanFeedSource(row)
}

func (s *SQLiteStore) ListFeedSources(ctx context.Context, enabledOnly bool) ([]model.FeedSource, error) {
	q := feedSourceSelectAll
	if enabledOnly {
		q += " WHERE enabled = 1"
	}
	q += " ORDER BY name"
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list feed_sources: %w", err)
	}
	defer rows.Close()
	var out []model.FeedSource
	for rows.Next() {
		src, err := scanFeedSourceFromRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *src)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpdateFeedSource(ctx context.Context, src *model.FeedSource) error {
	tagsJSON, _ := json.Marshal(src.DefaultTags)
	if len(tagsJSON) == 0 {
		tagsJSON = []byte("[]")
	}
	src.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE feed_sources SET
		    name = ?, kind = ?, url = ?, default_tags = ?, default_collection = ?,
		    auto_stash = ?, fetch_content = ?, poll_interval_minutes = ?, enabled = ?, updated_at = ?
		 WHERE id = ?`,
		src.Name, src.Kind, src.URL, string(tagsJSON), src.DefaultCollection,
		boolToInt(src.AutoStash), boolToInt(src.FetchContent),
		src.PollIntervalMinutes, boolToInt(src.Enabled),
		src.UpdatedAt.Unix(),
		src.ID,
	)
	if err != nil {
		return fmt.Errorf("update feed_source: %w", err)
	}
	return nil
}

func (s *SQLiteStore) DeleteFeedSource(ctx context.Context, idOrName string) error {
	src, err := s.GetFeedSource(ctx, idOrName)
	if err != nil {
		return err
	}
	// ON DELETE CASCADE on feed_candidates.source_id cleans up the
	// triage queue automatically.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM feed_sources WHERE id = ?`, src.ID); err != nil {
		return fmt.Errorf("delete feed_source: %w", err)
	}
	return nil
}

// TouchFeedSourcePoll records the last poll attempt. errMsg is the
// poller's error string ("" on success); preserved so the source-list
// UI can surface unhealthy sources.
func (s *SQLiteStore) TouchFeedSourcePoll(ctx context.Context, id int64, errMsg string) error {
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx,
		`UPDATE feed_sources SET last_polled_at = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		now, errMsg, now, id,
	)
	return err
}

// ───────────────────────────────────────────────────────────
// Feed candidates
// ───────────────────────────────────────────────────────────

// UpsertFeedCandidate inserts a new candidate when (source_id, guid) is
// unseen; updates title/description/thumbnail on an existing unread row
// (the upstream feed may edit entries). Returns created=true iff a new
// row was inserted. Triaged rows (state != 'unread') are left alone —
// once the user dismissed or stashed it, we don't second-guess.
func (s *SQLiteStore) UpsertFeedCandidate(ctx context.Context, c *model.FeedCandidate) (bool, error) {
	if c.SourceID == 0 || c.GUID == "" || c.URL == "" {
		return false, fmt.Errorf("source_id, guid, and url are required")
	}
	now := time.Now().UTC()
	if c.DiscoveredAt.IsZero() {
		c.DiscoveredAt = now
	}
	if c.StateChangedAt.IsZero() {
		c.StateChangedAt = now
	}
	if c.State == "" {
		c.State = model.FeedStateUnread
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO feed_candidates(source_id, guid, url, title, description, description_markdown, thumbnail_url, published_at, discovered_at, state, state_changed_at, snooze_until, stashed_item_id)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(source_id, guid) DO UPDATE SET
		    title                = CASE WHEN feed_candidates.state = 'unread' THEN excluded.title                ELSE feed_candidates.title                END,
		    description          = CASE WHEN feed_candidates.state = 'unread' THEN excluded.description          ELSE feed_candidates.description          END,
		    description_markdown = CASE WHEN feed_candidates.state = 'unread' THEN excluded.description_markdown ELSE feed_candidates.description_markdown END,
		    thumbnail_url        = CASE WHEN feed_candidates.state = 'unread' THEN excluded.thumbnail_url        ELSE feed_candidates.thumbnail_url        END`,
		c.SourceID, c.GUID, c.URL, c.Title, c.Description, c.DescriptionMarkdown, c.ThumbnailURL,
		nullableUnix(c.PublishedAt), c.DiscoveredAt.Unix(),
		c.State, c.StateChangedAt.Unix(),
		nullableUnix(c.SnoozeUntil),
		nullableString(c.StashedItemID),
	)
	if err != nil {
		return false, fmt.Errorf("upsert feed_candidate: %w", err)
	}
	n, _ := res.RowsAffected()
	created := n > 0
	// rowID is the existing id when conflicted; we don't need it here
	// since callers don't refer to c.ID after upsert.
	if created {
		if id, idErr := res.LastInsertId(); idErr == nil {
			c.ID = id
		}
	}
	return created, nil
}

func (s *SQLiteStore) GetFeedCandidate(ctx context.Context, id int64) (*model.FeedCandidate, error) {
	row := s.db.QueryRowContext(ctx, feedCandidateSelectByID, id)
	return scanFeedCandidate(row)
}

func (s *SQLiteStore) ListFeedCandidates(ctx context.Context, filter FeedCandidateFilter) ([]model.FeedCandidate, error) {
	states := filter.States
	if len(states) == 0 {
		states = []string{model.FeedStateUnread}
	}
	placeholders := make([]string, len(states))
	args := make([]any, 0, len(states)+2)
	for i, st := range states {
		placeholders[i] = "?"
		args = append(args, st)
	}
	where := []string{"fc.state IN (" + strings.Join(placeholders, ",") + ")"}
	if filter.SourceID > 0 {
		where = append(where, "fc.source_id = ?")
		args = append(args, filter.SourceID)
	}
	q := feedCandidateSelectAll + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY COALESCE(fc.published_at, fc.discovered_at) DESC"
	if filter.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list feed_candidates: %w", err)
	}
	defer rows.Close()
	var out []model.FeedCandidate
	for rows.Next() {
		c, err := scanFeedCandidateFromRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpdateFeedCandidateState(ctx context.Context, id int64, state string, snoozeUntil *time.Time, stashedItemID string) error {
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx,
		`UPDATE feed_candidates SET state = ?, state_changed_at = ?, snooze_until = ?, stashed_item_id = ? WHERE id = ?`,
		state, now, nullableUnix(snoozeUntil), nullableString(stashedItemID), id,
	)
	if err != nil {
		return fmt.Errorf("update feed_candidate state: %w", err)
	}
	return nil
}

// UpdateFeedCandidateMarkdown writes the cached Markdown form of a
// candidate's description. Used by the `reconvert` back-fill path
// and any future re-render flow.
func (s *SQLiteStore) UpdateFeedCandidateMarkdown(ctx context.Context, id int64, markdown string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE feed_candidates SET description_markdown = ? WHERE id = ?`,
		markdown, id,
	)
	return err
}

// UpdateFeedCandidateContent overwrites both description and
// description_markdown atomically. Used by the per-source
// `fetch_content` path that replaces a thin RSS abstract with the
// full readability-extracted article body.
func (s *SQLiteStore) UpdateFeedCandidateContent(ctx context.Context, id int64, description, markdown string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE feed_candidates SET description = ?, description_markdown = ? WHERE id = ?`,
		description, markdown, id,
	)
	return err
}

// ExpireSnoozedCandidates moves snoozed rows whose snooze_until has
// passed back to 'unread'. Called by the poller at refresh time so the
// Inbox surfaces them again without requiring a separate cron.
func (s *SQLiteStore) ExpireSnoozedCandidates(ctx context.Context, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE feed_candidates
		    SET state = 'unread', state_changed_at = ?, snooze_until = NULL
		  WHERE state = 'snoozed' AND snooze_until IS NOT NULL AND snooze_until <= ?`,
		now.UTC().Unix(), now.UTC().Unix(),
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ───────────────────────────────────────────────────────────
// Resurface
// ───────────────────────────────────────────────────────────

// PickResurfaceItems selects forgotten stash items for the Inbox's
// "From your stash" section. Eligibility filter:
//   - not archived
//   - capture is old enough to feel "forgotten" (updated_at < now - MinIdleAgo)
//   - hasn't been surfaced via Resurface within the last MinIdleAgo
//   - not currently snoozed
//   - not within the dismiss-cooldown window
//
// Scoring (computed in SQL) favors items that show curation effort:
//   +2  has notes
//   +1  has any tags (subquery)
//   +1  has any links (subquery)
//   -1  per ~year of age beyond 30 days (mild recency damping)
//
// Ties broken by random() so the order rotates between renders. We
// don't enforce type / tag diversity at this layer — the Inbox UI can
// apply that filter when rendering a small picked set.
func (s *SQLiteStore) PickResurfaceItems(ctx context.Context, params ResurfaceParams) ([]model.Item, error) {
	if params.Limit <= 0 {
		params.Limit = 5
	}
	if params.MinIdleAgo <= 0 {
		params.MinIdleAgo = 30 * 24 * time.Hour
	}
	if params.DismissCooldown <= 0 {
		params.DismissCooldown = 180 * 24 * time.Hour
	}
	now := time.Now().UTC()
	idleCutoff := now.Add(-params.MinIdleAgo)
	dismissCutoff := now.Add(-params.DismissCooldown)

	// Column list mirrors `scanItems` in sqlite.go — adding selected
	// columns here without updating that scanner would mis-index the
	// destination slice. Resurface state lives in a sidecar table
	// (item_resurface_state) joined LEFT so items without state still
	// participate in selection.
	// Resurface intentionally does NOT exclude queue-tagged items
	// (read-later / watch-later). Those items have their own Inbox
	// section but only the top-2-newest + bottom-1-oldest are shown
	// there; older commitments that don't fit need Resurface to
	// nag them back into view.
	q := `
SELECT i.id, i.type, i.title, i.url, i.notes,
       i.source_path, i.store_path, i.content_hash, i.extracted_text,
       i.mime_type, i.file_size, i.metadata, i.created_at, i.updated_at,
       i.archived, i.thumbnail_path
FROM items i
LEFT JOIN item_resurface_state rs ON rs.item_id = i.id
WHERE i.archived = 0
  AND i.updated_at < ?
  AND (rs.last_resurfaced_at IS NULL OR rs.last_resurfaced_at < ?)
  AND (rs.resurface_snoozed_until IS NULL OR rs.resurface_snoozed_until <= ?)
  AND (rs.resurface_dismissed_at IS NULL OR rs.resurface_dismissed_at < ?)
ORDER BY (
    (CASE WHEN TRIM(COALESCE(i.notes, '')) <> '' THEN 2 ELSE 0 END)
  + (CASE WHEN EXISTS(SELECT 1 FROM item_tags it WHERE it.item_id = i.id) THEN 1 ELSE 0 END)
  + (CASE WHEN EXISTS(SELECT 1 FROM item_links il WHERE il.item_id_from = i.id OR il.item_id_to = i.id) THEN 1 ELSE 0 END)
  - (CAST((? - julianday(i.created_at)) / 365 AS INTEGER))
) DESC, RANDOM()
LIMIT ?`
	nowJD := now.UTC().Format("2006-01-02 15:04:05")
	args := []any{
		idleCutoff.Format("2006-01-02 15:04:05"),
		idleCutoff.UTC().Unix(),
		now.Unix(),
		dismissCutoff.UTC().Unix(),
		nowJD,
		params.Limit,
	}
	return s.queryItems(ctx, q, args)
}

// MarkResurfaced upserts the sidecar row so a future PickResurfaceItems
// won't pick the same item again within MinIdleAgo.
func (s *SQLiteStore) MarkResurfaced(ctx context.Context, itemID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO item_resurface_state(item_id, last_resurfaced_at) VALUES(?, ?)
		 ON CONFLICT(item_id) DO UPDATE SET last_resurfaced_at = excluded.last_resurfaced_at`,
		itemID, now.UTC().Unix(),
	)
	return err
}

func (s *SQLiteStore) DismissResurface(ctx context.Context, itemID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO item_resurface_state(item_id, resurface_dismissed_at) VALUES(?, ?)
		 ON CONFLICT(item_id) DO UPDATE SET resurface_dismissed_at = excluded.resurface_dismissed_at`,
		itemID, now.UTC().Unix(),
	)
	return err
}

func (s *SQLiteStore) SnoozeResurface(ctx context.Context, itemID string, until time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO item_resurface_state(item_id, resurface_snoozed_until) VALUES(?, ?)
		 ON CONFLICT(item_id) DO UPDATE SET resurface_snoozed_until = excluded.resurface_snoozed_until`,
		itemID, until.UTC().Unix(),
	)
	return err
}

// ───────────────────────────────────────────────────────────
// scan helpers
// ───────────────────────────────────────────────────────────

const feedSourceSelectCols = `id, name, kind, url, default_tags, default_collection, auto_stash, fetch_content, poll_interval_minutes, enabled, last_polled_at, last_error, created_at, updated_at`

var (
	feedSourceSelectAll    = `SELECT ` + feedSourceSelectCols + ` FROM feed_sources`
	feedSourceSelectByID   = feedSourceSelectAll + ` WHERE id = ?`
	feedSourceSelectByName = feedSourceSelectAll + ` WHERE name = ?`
)

type rowOrRows interface {
	Scan(dest ...any) error
}

func scanFeedSource(r *sql.Row) (*model.FeedSource, error) {
	return scanFeedSourceInner(r)
}

func scanFeedSourceFromRows(r *sql.Rows) (*model.FeedSource, error) {
	return scanFeedSourceInner(r)
}

func scanFeedSourceInner(r rowOrRows) (*model.FeedSource, error) {
	var src model.FeedSource
	var (
		tagsJSON         string
		autoStashInt     int
		fetchContentInt  int
		enabledInt       int
		lastPolledUnix   sql.NullInt64
		createdUnix      int64
		updatedUnix      int64
	)
	err := r.Scan(
		&src.ID, &src.Name, &src.Kind, &src.URL,
		&tagsJSON, &src.DefaultCollection,
		&autoStashInt, &fetchContentInt,
		&src.PollIntervalMinutes, &enabledInt,
		&lastPolledUnix, &src.LastError,
		&createdUnix, &updatedUnix,
	)
	if err != nil {
		return nil, err
	}
	src.AutoStash = autoStashInt != 0
	src.FetchContent = fetchContentInt != 0
	src.Enabled = enabledInt != 0
	if tagsJSON != "" {
		_ = json.Unmarshal([]byte(tagsJSON), &src.DefaultTags)
	}
	if lastPolledUnix.Valid {
		t := time.Unix(lastPolledUnix.Int64, 0).UTC()
		src.LastPolledAt = &t
	}
	src.CreatedAt = time.Unix(createdUnix, 0).UTC()
	src.UpdatedAt = time.Unix(updatedUnix, 0).UTC()
	return &src, nil
}

const feedCandidateSelectCols = `fc.id, fc.source_id, fs.name AS source_name, fc.guid, fc.url, fc.title, fc.description, fc.description_markdown, fc.thumbnail_url, fc.published_at, fc.discovered_at, fc.state, fc.state_changed_at, fc.snooze_until, fc.stashed_item_id`

var (
	feedCandidateSelectAll  = `SELECT ` + feedCandidateSelectCols + ` FROM feed_candidates fc JOIN feed_sources fs ON fc.source_id = fs.id`
	feedCandidateSelectByID = feedCandidateSelectAll + ` WHERE fc.id = ?`
)

func scanFeedCandidate(r *sql.Row) (*model.FeedCandidate, error) {
	return scanFeedCandidateInner(r)
}

func scanFeedCandidateFromRows(r *sql.Rows) (*model.FeedCandidate, error) {
	return scanFeedCandidateInner(r)
}

func scanFeedCandidateInner(r rowOrRows) (*model.FeedCandidate, error) {
	var c model.FeedCandidate
	var (
		publishedUnix    sql.NullInt64
		discoveredUnix   int64
		stateChangedUnix int64
		snoozeUnix       sql.NullInt64
		stashedID        sql.NullString
	)
	err := r.Scan(
		&c.ID, &c.SourceID, &c.SourceName,
		&c.GUID, &c.URL, &c.Title, &c.Description, &c.DescriptionMarkdown, &c.ThumbnailURL,
		&publishedUnix, &discoveredUnix,
		&c.State, &stateChangedUnix,
		&snoozeUnix, &stashedID,
	)
	if err != nil {
		return nil, err
	}
	if publishedUnix.Valid {
		t := time.Unix(publishedUnix.Int64, 0).UTC()
		c.PublishedAt = &t
	}
	c.DiscoveredAt = time.Unix(discoveredUnix, 0).UTC()
	c.StateChangedAt = time.Unix(stateChangedUnix, 0).UTC()
	if snoozeUnix.Valid {
		t := time.Unix(snoozeUnix.Int64, 0).UTC()
		c.SnoozeUntil = &t
	}
	if stashedID.Valid {
		c.StashedItemID = stashedID.String
	}
	return &c, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableUnix(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Unix()
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
