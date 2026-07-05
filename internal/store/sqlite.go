package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/msjurset/gostash/internal/model"

	_ "modernc.org/sqlite"
)

const itemColumns = "id, type, title, url, notes, source_path, store_path, content_hash, extracted_text, mime_type, file_size, metadata, created_at, updated_at, archived, thumbnail_path, latitude, longitude, location_source, captured_at, chat_history, caption, speaker_map"

// SQLiteStore implements Store using SQLite with FTS5.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLite opens (or creates) a SQLite database and runs migrations.
func NewSQLite(dsn string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// `busy_timeout` MUST come first. PRAGMAs run sequentially with
	// no implicit retry; if another `stash` process is mid-write
	// when we open, the very first statement gets a "database is
	// locked" error before busy_timeout would have given it 5s to
	// wait. Setting busy_timeout up front protects everything that
	// follows — including journal_mode=WAL, which itself takes a
	// brief exclusive lock to flip the mode.
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %s: %w", pragma, err)
		}
	}

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		data, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		if _, err := s.db.Exec(string(data)); err != nil {
			return fmt.Errorf("exec migration %s: %w", e.Name(), err)
		}
	}

	// Ensure the items CHECK constraint includes 'email' for pre-v0.9 databases.
	if err := s.migrateEmailType(); err != nil {
		return fmt.Errorf("migrate email type: %w", err)
	}

	// Add the archived column if it's not already present. The .sql
	// migration runner above re-runs every file on every startup with
	// no tracking, so ALTER TABLE ADD COLUMN can't live there — it
	// would fail with "duplicate column name" on the second startup.
	if err := s.migrateArchivedColumn(); err != nil {
		return fmt.Errorf("migrate archived column: %w", err)
	}
	if err := s.migrateSavedSearchLive(); err != nil {
		return fmt.Errorf("migrate saved_searches.live: %w", err)
	}
	if err := s.migrateThumbnailPath(); err != nil {
		return fmt.Errorf("migrate thumbnail_path: %w", err)
	}
	if err := s.migrateItemCollectionsPosition(); err != nil {
		return fmt.Errorf("migrate item_collections.position: %w", err)
	}
	if err := s.migrateFeedCandidateMarkdown(); err != nil {
		return fmt.Errorf("migrate feed_candidates.description_markdown: %w", err)
	}
	if err := s.migrateFeedSourceFetchContent(); err != nil {
		return fmt.Errorf("migrate feed_sources.fetch_content: %w", err)
	}
	if err := s.migrateItemLocation(); err != nil {
		return fmt.Errorf("migrate items.location: %w", err)
	}
	if err := s.migrateItemFiles(); err != nil {
		return fmt.Errorf("migrate item_files: %w", err)
	}
	if err := s.migrateItemCapturedAt(); err != nil {
		return fmt.Errorf("migrate items.captured_at: %w", err)
	}
	if err := s.migrateItemCaption(); err != nil {
		return fmt.Errorf("migrate items.caption: %w", err)
	}
	if err := s.migrateDismissedMoments(); err != nil {
		return fmt.Errorf("migrate dismissed_moments: %w", err)
	}
	if err := s.migrateCollectionUsageSignals(); err != nil {
		return fmt.Errorf("migrate collection usage signals: %w", err)
	}
	if err := s.migrateItemChatHistory(); err != nil {
		return fmt.Errorf("migrate items.chat_history: %w", err)
	}
	if err := s.migrateItemSpeakerMap(); err != nil {
		return fmt.Errorf("migrate items.speaker_map: %w", err)
	}

	return nil
}

// migrateItemChatHistory adds the chat_history column to items.
// Stored as a JSON string. Idempotent.
func (s *SQLiteStore) migrateItemChatHistory() error {
	return s.addColumnIfMissing("items", "chat_history",
		`ALTER TABLE items ADD COLUMN chat_history TEXT NOT NULL DEFAULT '[]'`,
	)
}

// migrateItemSpeakerMap adds the speaker_map column to items for diarization metadata.
// Stored as a JSON string. Idempotent.
func (s *SQLiteStore) migrateItemSpeakerMap() error {
	return s.addColumnIfMissing("items", "speaker_map",
		`ALTER TABLE items ADD COLUMN speaker_map TEXT NOT NULL DEFAULT '{}'`,
	)
}

// migrateCollectionUsageSignals adds the two columns that back the
// "Recent" and "Frequent" sidebar sort modes:
//   - item_collections.added_at — when an item was added to a
//     collection. Recent = newest MAX(added_at) per collection.
//     Backfilled to items.created_at for legacy rows so the sort
//     stays useful before any new adds happen.
//   - collections.view_count — incremented when the user navigates
//     to a collection. Frequent = highest view_count.
// Idempotent.
func (s *SQLiteStore) migrateCollectionUsageSignals() error {
	// SQLite ALTER TABLE ADD COLUMN can't take a non-constant
	// default like CURRENT_TIMESTAMP — that's a CREATE TABLE-only
	// privilege. So the column is nullable, backfilled from the
	// underlying item's created_at as a reasonable proxy for
	// "when did this membership begin", and new rows get
	// CURRENT_TIMESTAMP populated explicitly by AddToCollection.
	if err := s.addColumnIfMissing("item_collections", "added_at",
		`ALTER TABLE item_collections ADD COLUMN added_at TIMESTAMP`,
		`UPDATE item_collections
		 SET added_at = (
			SELECT created_at FROM items WHERE items.id = item_collections.item_id
		 )
		 WHERE added_at IS NULL`,
	); err != nil {
		return err
	}
	if _, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_item_collections_added_at
		 ON item_collections(collection_id, added_at DESC)`,
	); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("collections", "view_count",
		`ALTER TABLE collections ADD COLUMN view_count INTEGER NOT NULL DEFAULT 0`,
	); err != nil {
		return err
	}
	return nil
}

// migrateItemCaption adds the optional caption column to items.
// Backs the "caption for the primary file" intent in multi-file
// carousels, letting the user name the cover ("male", "side view")
// without hijacking the item title.
func (s *SQLiteStore) migrateItemCaption() error {
	return s.addColumnIfMissing("items", "caption",
		`ALTER TABLE items ADD COLUMN caption TEXT NOT NULL DEFAULT ''`)
}

// migrateDismissedMoments backs the user's "I don't want this
// cluster" votes on Moments suggestions. The signature column is
// SHA-256 of the cluster's sorted item-ID set — stable across
// recomputes so dismissals survive across CLI/UI sessions, but
// changes when the underlying item set does (so removing an item
// from the cluster naturally re-surfaces the new shape). Idempotent.
func (s *SQLiteStore) migrateDismissedMoments() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS dismissed_moments (
			signature    TEXT PRIMARY KEY,
			dismissed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			item_count   INTEGER  NOT NULL,
			sample_title TEXT     NOT NULL DEFAULT ''
		)
	`)
	return err
}

// migrateItemCapturedAt adds the optional captured_at column that
// records when the underlying content was created in the real world
// — e.g. when a photo was shot (from EXIF DateTimeOriginal), when a
// file was last modified on disk before stashing, when an email's
// most recent thread reply was sent. Distinct from items.created_at,
// which records when the row was inserted into the stash. NULL means
// "no signal available" (URL items, items where EXIF couldn't be
// read, etc.) — consumers like trip clustering fall back to
// created_at in that case. Idempotent.
func (s *SQLiteStore) migrateItemCapturedAt() error {
	if err := s.addColumnIfMissing("items", "captured_at",
		`ALTER TABLE items ADD COLUMN captured_at TIMESTAMP`); err != nil {
		return err
	}
	// Partial index — most non-image, non-file items will have NULL
	// captured_at forever. Index only the populated rows so range
	// queries (e.g. moments clustering) stay cheap.
	_, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_items_captured_at
		 ON items(captured_at) WHERE captured_at IS NOT NULL`,
	)
	return err
}

// migrateItemFiles introduces the item_files sidecar table that
// holds additional attached photos beyond the primary store_path.
// items.store_path remains the cover so existing read paths work
// unchanged; rows in item_files accumulate only when the user
// attaches extra angles / states of the same subject. Idempotent.
func (s *SQLiteStore) migrateItemFiles() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS item_files (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			item_id       TEXT    NOT NULL REFERENCES items(id) ON DELETE CASCADE,
			store_path    TEXT    NOT NULL,
			content_hash  TEXT    NOT NULL,
			mime_type     TEXT    NOT NULL DEFAULT '',
			file_size     INTEGER NOT NULL DEFAULT 0,
			caption       TEXT    NOT NULL DEFAULT '',
			position      INTEGER NOT NULL DEFAULT 0,
			created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
			UNIQUE(item_id, content_hash)
		)
	`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_item_files_item ON item_files(item_id, position)`,
	)
	return err
}

// migrateItemLocation adds the latitude/longitude/location_source
// columns that back model.Item.Location. Populated automatically
// from JPEG EXIF on image capture, on mobile location-API capture,
// or set manually via `stash edit --location`. NULL latitude is the
// "no location" sentinel (so existing rows pre-migration are
// indistinguishable from genuinely-locationless items). Partial
// index on (lat, lon) so `WHERE latitude IS NOT NULL` queries stay
// cheap as the items table grows. Idempotent.
func (s *SQLiteStore) migrateItemLocation() error {
	if err := s.addColumnIfMissing("items", "latitude",
		`ALTER TABLE items ADD COLUMN latitude REAL`); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("items", "longitude",
		`ALTER TABLE items ADD COLUMN longitude REAL`); err != nil {
		return err
	}
	if err := s.addColumnIfMissing("items", "location_source",
		`ALTER TABLE items ADD COLUMN location_source TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_items_location
		 ON items(latitude, longitude) WHERE latitude IS NOT NULL`,
	)
	return err
}

// migrateFeedSourceFetchContent adds the `fetch_content` opt-in flag.
// When true, the poller follows each new candidate's URL through the
// same readability-extraction path `stash refresh` uses, then writes
// the result back to `description` + `description_markdown`. Lets
// thin-description feeds (Hacker News, aggregators that ship just a
// title and link) produce inbox-rich content. Idempotent.
func (s *SQLiteStore) migrateFeedSourceFetchContent() error {
	return s.addColumnIfMissing("feed_sources", "fetch_content",
		`ALTER TABLE feed_sources ADD COLUMN fetch_content INTEGER NOT NULL DEFAULT 0`)
}

// migrateFeedCandidateMarkdown caches the Markdown-rendered form of
// each candidate's description alongside the raw HTML. Populated
// once at poll-time so the Mac app's Inbox preview pane renders
// instantly without spawning a CLI roundtrip. Idempotent.
func (s *SQLiteStore) migrateFeedCandidateMarkdown() error {
	return s.addColumnIfMissing("feed_candidates", "description_markdown",
		`ALTER TABLE feed_candidates ADD COLUMN description_markdown TEXT NOT NULL DEFAULT ''`)
}

// migrateItemCollectionsPosition adds the position column that backs
// curated ordering within a collection. Existing rows get back-filled
// per-collection by item.created_at DESC (newest first → position 0)
// so behavior pre-and-post-migration looks the same. Idempotent.
func (s *SQLiteStore) migrateItemCollectionsPosition() error {
	return s.addColumnIfMissing("item_collections", "position",
		`ALTER TABLE item_collections ADD COLUMN position INTEGER NOT NULL DEFAULT 0`,
		`UPDATE item_collections AS ic
		 SET position = (
		     SELECT COUNT(*)
		     FROM item_collections ic2
		     JOIN items i2 ON i2.id = ic2.item_id
		     JOIN items i ON i.id = ic.item_id
		     WHERE ic2.collection_id = ic.collection_id
		       AND i2.created_at > i.created_at
		 )`,
		`CREATE INDEX IF NOT EXISTS idx_item_collections_collection_position
		    ON item_collections(collection_id, position)`,
	)
}

// migrateThumbnailPath adds the items.thumbnail_path column that stores
// the path of a per-item thumbnail relative to the files dir. Empty
// string means "no thumbnail". Idempotent — checks pragma_table_info.
func (s *SQLiteStore) migrateThumbnailPath() error {
	return s.addColumnIfMissing("items", "thumbnail_path",
		`ALTER TABLE items ADD COLUMN thumbnail_path TEXT NOT NULL DEFAULT ''`,
	)
}

// migrateArchivedColumn adds the items.archived flag for soft-delete
// semantics. Idempotent — checks pragma_table_info first.
func (s *SQLiteStore) migrateArchivedColumn() error {
	return s.addColumnIfMissing("items", "archived",
		`ALTER TABLE items ADD COLUMN archived INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_items_archived ON items(archived)`,
	)
}

// migrateSavedSearchLive adds the saved_searches.live flag that drives
// auto-refreshing Smart Collection sidebar entries in stash-mac.
// Idempotent — checks pragma_table_info first.
func (s *SQLiteStore) migrateSavedSearchLive() error {
	return s.addColumnIfMissing("saved_searches", "live",
		`ALTER TABLE saved_searches ADD COLUMN live INTEGER NOT NULL DEFAULT 0`,
	)
}

// addColumnIfMissing runs the given DDL statements if `column` doesn't
// already exist on `table`. SQLite has no IF NOT EXISTS clause for
// ALTER TABLE ADD COLUMN, so we gate on pragma_table_info to keep the
// migration idempotent across repeated startups.
func (s *SQLiteStore) addColumnIfMissing(table, column string, stmts ...string) error {
	var present int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`,
		table, column,
	).Scan(&present)
	if err != nil {
		return err
	}
	if present > 0 {
		return nil
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// migrateEmailType adds 'email' to the items type CHECK constraint if missing.
func (s *SQLiteStore) migrateEmailType() error {
	var schema string
	err := s.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='items'").Scan(&schema)
	if err != nil || strings.Contains(schema, "'email'") {
		return nil // already includes email or table doesn't exist
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE items_mig (
			id TEXT PRIMARY KEY, type TEXT NOT NULL CHECK(type IN ('link','snippet','file','image','email')),
			title TEXT NOT NULL DEFAULT '', url TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '',
			source_path TEXT NOT NULL DEFAULT '', store_path TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '', extracted_text TEXT NOT NULL DEFAULT '',
			mime_type TEXT NOT NULL DEFAULT '', file_size INTEGER NOT NULL DEFAULT 0,
			metadata TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at DATETIME NOT NULL DEFAULT (datetime('now')))`,
		`INSERT INTO items_mig SELECT * FROM items`,
		`DROP TABLE items`,
		`ALTER TABLE items_mig RENAME TO items`,
		`CREATE INDEX IF NOT EXISTS idx_items_type ON items(type)`,
		`CREATE INDEX IF NOT EXISTS idx_items_url ON items(url)`,
		`CREATE INDEX IF NOT EXISTS idx_items_content_hash ON items(content_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_items_created_at ON items(created_at)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt[:40], err)
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// Checkpoint flushes the WAL to the main database file.
// ListItemsMissingEmbeddings returns items that don't have a record in
// item_embeddings. Ordered by created_at DESC.
func (s *SQLiteStore) ListItemsMissingEmbeddings(ctx context.Context, limit int) ([]model.Item, error) {
	if limit <= 0 {
		limit = 50
	}
	// Query for items that don't have an embedding entry.
	q := `SELECT i.* FROM items i
	      LEFT JOIN item_embeddings e ON e.item_id = i.id
	      WHERE e.item_id IS NULL AND i.archived = 0
	      ORDER BY i.created_at DESC LIMIT ?`
	return s.queryItems(ctx, q, []any{limit})
}

// SaveItemEmbedding inserts or updates an embedding for an item.
func (s *SQLiteStore) SaveItemEmbedding(ctx context.Context, itemID string, modelName string, vector []float32) error {
	blob := float32ToBytes(vector)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO item_embeddings (item_id, model, vector, updated_at)
		 VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(item_id) DO UPDATE SET model=excluded.model, vector=excluded.vector, updated_at=excluded.updated_at`,
		itemID, modelName, blob)
	return err
}

// GetItemEmbedding retrieves an embedding for an item.
func (s *SQLiteStore) GetItemEmbedding(ctx context.Context, itemID string) (string, []float32, error) {
	var modelName string
	var blob []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT model, vector FROM item_embeddings WHERE item_id = ?`, itemID,
	).Scan(&modelName, &blob)
	if err != nil {
		return "", nil, err
	}
	return modelName, bytesToFloat32(blob), nil
}

// SearchSemantic performs a brute-force cosine similarity search
// against stored embeddings. Respects non-query filters.
func (s *SQLiteStore) SearchSemantic(ctx context.Context, queryVector []float32, filter model.ItemFilter) ([]model.Item, error) {
	// 1. Load all candidate items that match the filters (but ignore Query)
	candidateFilter := filter
	candidateFilter.Query = ""
	candidateFilter.Limit = 0 // get all candidates
	candidates, err := s.ListItems(ctx, candidateFilter)
	if err != nil {
		return nil, err
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// 2. Load all embeddings in one batch
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, vector FROM item_embeddings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	vectorMap := make(map[string][]float32)
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err == nil {
			vectorMap[id] = bytesToFloat32(blob)
		}
	}

	// 3. Score candidates
	type score struct {
		item  model.Item
		score float32
	}
	var scores []score
	for _, item := range candidates {
		v, ok := vectorMap[item.ID]
		if !ok {
			continue
		}
		scores = append(scores, score{item: item, score: cosineSimilarity(queryVector, v)})
	}

	// 4. Sort and limit
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(scores) > limit {
		scores = scores[:limit]
	}

	var items []model.Item
	for _, sc := range scores {
		items = append(items, sc.item)
	}

	return items, nil
}

// SearchHybrid combines FTS and Semantic results using Reciprocal Rank
// Fusion (RRF). Best of both worlds: keyword precision + semantic
// discovery.
func (s *SQLiteStore) SearchHybrid(ctx context.Context, filter model.ItemFilter) ([]model.Item, error) {
	// 1. Get FTS results
	ftsFilter := filter
	ftsFilter.Semantic = false
	ftsFilter.Limit = 100 // pull enough for ranking
	ftsItems, err := s.SearchItems(ctx, ftsFilter)
	if err != nil {
		return nil, err
	}

	// 2. Get Semantic results
	semanticItems, err := s.SearchSemantic(ctx, filter.QueryVector, filter)
	if err != nil {
		return nil, err
	}

	// 3. RRF Blending
	const k = 60.0
	scores := make(map[string]float64)
	itemMap := make(map[string]model.Item)

	for i, item := range ftsItems {
		scores[item.ID] += 1.0 / (k + float64(i+1))
		itemMap[item.ID] = item
	}
	for i, item := range semanticItems {
		scores[item.ID] += 1.0 / (k + float64(i+1))
		itemMap[item.ID] = item
	}

	// 4. Sort and return
	type itemScore struct {
		id    string
		score float64
	}
	var ranked []itemScore
	for id, score := range scores {
		ranked = append(ranked, itemScore{id: id, score: score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	var result []model.Item
	for _, rs := range ranked {
		if item, ok := itemMap[rs.id]; ok {
			result = append(result, item)
		}
	}
	return result, nil
}

func float32ToBytes(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func bytesToFloat32(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4 : i*4+4]))
	}
	return v
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

func (s *SQLiteStore) Checkpoint() error {
	_, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// prefixQuery converts a plain search string into an FTS5 prefix query
// so that partial words match. "hello wor" becomes "hello* wor*".
// Words that already end with * are left as-is.
//
// Sanitization step: FTS5 query syntax rejects bare punctuation
// (`?`, `!`, `:`, `(`, `)`, `"`, etc.) — they're either operators or
// invalid characters depending on the version, and an FTS5
// "syntax error near \"?\"" propagates straight to the Mac app as a
// generic "data couldn't be read" dialog. We strip everything
// that's not a letter/digit/whitespace/underscore-or-hyphen from
// each word, then prefix-match what's left. Loses the ability to
// search for literal punctuation (no `what?` literal-match), but
// FTS5's default tokenizer would have dropped those characters at
// index time anyway, so the result set is the same.
func prefixQuery(q string) string {
	cleaned := ftsSanitize(q)
	words := strings.Fields(cleaned)
	for i, w := range words {
		// Wrap in quotes so leading hyphens or other FTS5 special chars
		// don't trigger "no such column" or operator errors. FTS5
		// supports the "term"* syntax for prefix matches.
		words[i] = fmt.Sprintf("\"%s\"*", w)
	}
	return strings.Join(words, " ")
}

// ftsSanitize keeps letters, digits, whitespace, underscore, and
// hyphen. Everything else is replaced with a space. Letter/digit are
// matched against the full Unicode tables so non-ASCII queries still
// work (Cyrillic, accented Latin, CJK, etc.).
func ftsSanitize(q string) string {
	var b strings.Builder
	b.Grow(len(q))
	for _, r := range q {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r),
			unicode.IsSpace(r), r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return b.String()
}

// CreateItem inserts a new item and its tags/collections.
func (s *SQLiteStore) CreateItem(ctx context.Context, item *model.Item) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	meta, err := marshalMeta(item.Metadata)
	if err != nil {
		return err
	}

	chatHist, err := json.Marshal(item.ChatHistory)
	if err != nil {
		chatHist = []byte("[]")
	}

	speakerMap, err := json.Marshal(item.SpeakerMap)
	if err != nil {
		speakerMap = []byte("{}")
	}

	lat, lon, locSrc := splitLocation(item.Location)
	captured := ptrToNullTime(item.CapturedAt)
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO items (%s)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, itemColumns),
		item.ID, item.Type, item.Title, item.URL, item.Notes, item.SourcePath,
		item.StorePath, item.ContentHash, item.ExtractedText, item.MimeType,
		item.FileSize, meta, item.CreatedAt, item.UpdatedAt,
		0, // archived
		item.ThumbnailPath,
		lat, lon, locSrc, captured, string(chatHist),
		item.Caption,
		string(speakerMap),
	)
	if err != nil {
		return fmt.Errorf("insert item: %w", err)
	}

	if err := s.setTags(ctx, tx, item.ID, item.Tags); err != nil {
		return err
	}
	for _, c := range item.Collections {
		if err := s.addToCollectionTx(ctx, tx, item.ID, c.Name); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// GetItem fetches a single item by ID with its tags and collections.
func (s *SQLiteStore) GetItem(ctx context.Context, id string) (*model.Item, error) {
	// Try exact match first, then prefix match for short IDs
	query := fmt.Sprintf("SELECT %s FROM items WHERE id = ?", itemColumns)
	row := s.db.QueryRowContext(ctx, query, id)
	item, err := s.scanItem(row)
	if err == sql.ErrNoRows && len(id) >= 6 {
		query = fmt.Sprintf("SELECT %s FROM items WHERE id LIKE ?", itemColumns)
		row = s.db.QueryRowContext(ctx, query, id+"%")
		item, err = s.scanItem(row)
	}
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("item not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get item: %w", err)
	}
	if err := s.loadRelations(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

// ListItems returns items matching the filter, ordered by creation time descending.
//
// When a regex filter is set, the SQL LIMIT is deferred and applied
// post-regex. Otherwise, "list 3 items matching /github/" would fetch
// the 3 newest items and then drop any that didn't match — almost
// always producing zero results when the user expected "the 3 newest
// of all matching items."
func (s *SQLiteStore) ListItems(ctx context.Context, filter model.ItemFilter) ([]model.Item, error) {
	regex := strings.TrimSpace(filter.Regex)
	requestedLimit := filter.Limit
	if regex != "" {
		filter.Limit = 0
	}
	q, args := s.buildListQuery(filter)
	items, err := s.queryItems(ctx, q, args)
	if err != nil {
		return nil, err
	}
	items, err = applyRegexFilter(items, regex)
	if err != nil {
		return nil, err
	}
	if regex != "" && requestedLimit > 0 && len(items) > requestedLimit {
		items = items[:requestedLimit]
	}
	return items, nil
}

// SearchItems performs full-text search using FTS5.
func (s *SQLiteStore) SearchItems(ctx context.Context, filter model.ItemFilter) ([]model.Item, error) {
	if filter.Semantic && len(filter.QueryVector) > 0 {
		return s.SearchHybrid(ctx, filter)
	}
	if filter.Query == "" {
		return s.ListItems(ctx, filter)
	}

	// Sanitize first so we can detect "all punctuation" queries (e.g.
	// a lone `"` or `?`). FTS5 errors on an empty MATCH expression, so
	// after stripping operators we may be left with nothing usable —
	// in that case we fall back to ListItems with whatever non-text
	// filters were also set (tag, type, etc.). A query of only `"`
	// effectively means "no search yet"; the user is mid-typing.
	ftsQuery := prefixQuery(filter.Query)
	if strings.TrimSpace(ftsQuery) == "" {
		return s.ListItems(ctx, filter)
	}

	var where []string
	var args []any

	// Build FTS5 query with prefix wildcards so partial words match.
	// Also match items whose tags contain any of the search words.
	// Tag LIKE uses the SAME sanitized words so a `"` in the search
	// query doesn't poison the tag pattern either.
	words := strings.Fields(ftsSanitize(filter.Query))
	tagLikes := make([]string, len(words))
	var tagArgs []any
	for i, w := range words {
		tagLikes[i] = "t.name LIKE ?"
		tagArgs = append(tagArgs, "%"+w+"%")
	}
	where = append(where, fmt.Sprintf(`(i.rowid IN (SELECT rowid FROM items_fts WHERE items_fts MATCH ?)
		OR i.id IN (SELECT it.item_id FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE %s))`,
		strings.Join(tagLikes, " OR ")))
	args = append(args, ftsQuery)
	args = append(args, tagArgs...)

	if filter.Type != "" {
		if filter.Type == "audio" {
			where = append(where, "(i.type = 'file' AND i.mime_type LIKE 'audio/%')")
		} else {
			where = append(where, "i.type = ?")
			args = append(args, filter.Type)
		}
	}
	if filter.Untagged {
		where = append(where, "NOT EXISTS (SELECT 1 FROM item_tags it WHERE it.item_id = i.id)")
	} else {
		if len(filter.Tags) > 0 {
			placeholders := make([]string, len(filter.Tags))
			for i, t := range filter.Tags {
				placeholders[i] = "?"
				args = append(args, t)
			}
			where = append(where, fmt.Sprintf(
				"i.id IN (SELECT it.item_id FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE t.name IN (%s))",
				strings.Join(placeholders, ","),
			))
		}
		if len(filter.ExcludeTags) > 0 {
			placeholders := make([]string, len(filter.ExcludeTags))
			for i, t := range filter.ExcludeTags {
				placeholders[i] = "?"
				args = append(args, t)
			}
			where = append(where, fmt.Sprintf(
				"i.id NOT IN (SELECT it.item_id FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE t.name IN (%s))",
				strings.Join(placeholders, ","),
			))
		}
	}
	if filter.Collection != "" {
		where = append(where, "i.id IN (SELECT ic.item_id FROM item_collections ic JOIN collections c ON c.id = ic.collection_id WHERE c.name = ?)")
		args = append(args, filter.Collection)
	}
	if filter.After != nil {
		where = append(where, "i.created_at >= ?")
		args = append(args, *filter.After)
	}
	if filter.Before != nil {
		where = append(where, "i.created_at <= ?")
		args = append(args, *filter.Before)
	}
	if t, ok := resolveRecent(filter.Recent); ok {
		where = append(where, "i.created_at >= ?")
		args = append(args, t)
	}
	if filter.OnlyArchived {
		where = append(where, "i.archived = 1")
	} else if !filter.IncludeArchived {
		where = append(where, "i.archived = 0")
	}

	q := fmt.Sprintf("SELECT %s FROM items i WHERE %s ORDER BY i.created_at DESC",
		prefixColumns("i", itemColumns), strings.Join(where, " AND "))

	regex := strings.TrimSpace(filter.Regex)
	requestedLimit := filter.Limit
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	// Skip the SQL LIMIT when a regex filter is active so post-regex
	// truncation reflects "top N of all matches" rather than "of the
	// top N rows, those that match" (which is almost always empty).
	if regex == "" {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	if filter.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	items, err := s.queryItems(ctx, q, args)
	if err != nil {
		return nil, err
	}
	items, err = applyRegexFilter(items, regex)
	if err != nil {
		return nil, err
	}
	if regex != "" {
		effective := requestedLimit
		if effective <= 0 {
			effective = 50
		}
		if len(items) > effective {
			items = items[:effective]
		}
	}
	return items, nil
}

// UpdateItem updates an existing item.
func (s *SQLiteStore) UpdateItem(ctx context.Context, item *model.Item) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	meta, err := marshalMeta(item.Metadata)
	if err != nil {
		return err
	}

	item.UpdatedAt = time.Now().UTC()

	chatHist, err := json.Marshal(item.ChatHistory)
	if err != nil {
		chatHist = []byte("[]")
	}

	speakerMap, err := json.Marshal(item.SpeakerMap)
	if err != nil {
		speakerMap = []byte("{}")
	}

	lat, lon, locSrc := splitLocation(item.Location)
	captured := ptrToNullTime(item.CapturedAt)
	res, err := tx.ExecContext(ctx, `
		UPDATE items SET type=?, title=?, url=?, notes=?, source_path=?, store_path=?,
			content_hash=?, extracted_text=?, mime_type=?, file_size=?, metadata=?, updated_at=?,
			thumbnail_path=?, latitude=?, longitude=?, location_source=?, captured_at=?, chat_history=?, archived=?,
			caption=?, speaker_map=?
		WHERE id=?`,
		item.Type, item.Title, item.URL, item.Notes, item.SourcePath, item.StorePath,
		item.ContentHash, item.ExtractedText, item.MimeType, item.FileSize,
		meta, item.UpdatedAt, item.ThumbnailPath,
		lat, lon, locSrc, captured, string(chatHist), item.Archived,
		item.Caption,
		string(speakerMap),
		item.ID,
	)
	if err != nil {
		return fmt.Errorf("update item: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("item not found: %s", item.ID)
	}

	if err := s.setTags(ctx, tx, item.ID, item.Tags); err != nil {
		return err
	}

	return tx.Commit()
}

// DeleteItem removes an item and all its associations.
func (s *SQLiteStore) DeleteItem(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete item: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("item not found: %s", id)
	}
	return nil
}

// SetArchived flips the archived flag on a single item. Soft-delete
// semantics: archived items are excluded from list/search by default
// but otherwise untouched (file blob, tags, links, collections all
// remain). Use `--include-archived` / `--archived` on list to view.
func (s *SQLiteStore) SetArchived(ctx context.Context, id string, archived bool) error {
	val := 0
	if archived {
		val = 1
	}
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE items SET archived = ?, updated_at = datetime('now') WHERE id = ?`,
		val, id,
	)
	if err != nil {
		return fmt.Errorf("set archived: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Try short-id prefix match — same as GetItem.
		if len(id) >= 6 {
			res2, err := s.db.ExecContext(
				ctx,
				`UPDATE items SET archived = ?, updated_at = datetime('now') WHERE id LIKE ?`,
				val, id+"%",
			)
			if err != nil {
				return fmt.Errorf("set archived: %w", err)
			}
			if n2, _ := res2.RowsAffected(); n2 > 0 {
				return nil
			}
		}
		return fmt.Errorf("item not found: %s", id)
	}
	return nil
}

// ExistsByURL checks whether an item with the given URL already exists.
func (s *SQLiteStore) ExistsByURL(ctx context.Context, url string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE url = ?`, url).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check url: %w", err)
	}
	return count > 0, nil
}

// GetItemByURL fetches the first item matching the given URL.
func (s *SQLiteStore) GetItemByURL(ctx context.Context, url string) (*model.Item, error) {
	row := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT %s FROM items WHERE url = ? LIMIT 1", itemColumns), url)
	item, err := s.scanItem(row)
	if err != nil {
		return nil, fmt.Errorf("get item by url: %w", err)
	}
	if err := s.loadRelations(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

// GetItemByContentHash fetches the first item with the given content
// hash. Used by the rules engine's duplicate-detection pre-check at
// capture time. Returns sql.ErrNoRows if no item has that hash —
// callers should treat that as "not a duplicate" rather than an error.
func (s *SQLiteStore) GetItemByContentHash(ctx context.Context, hash string) (*model.Item, error) {
	if hash == "" {
		return nil, sql.ErrNoRows
	}
	row := s.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT %s FROM items WHERE content_hash = ? AND archived = 0 LIMIT 1", itemColumns), hash)
	item, err := s.scanItem(row)
	if err != nil {
		return nil, err
	}
	if err := s.loadRelations(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

// CountItemsByContentHash returns how many items (archived + live)
// reference the given content hash. Used by the delete paths to
// decide whether the on-disk blob is safe to remove — a positive
// count after the item's row is gone means another item still
// shares the bytes, and yanking the file would leave a dangling
// reference. Hash "" always returns 0.
func (s *SQLiteStore) CountItemsByContentHash(ctx context.Context, hash string) (int, error) {
	if hash == "" {
		return 0, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM items WHERE content_hash = ?`, hash).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count by content hash: %w", err)
	}
	return n, nil
}

// ListURLsWithoutContent returns URL items that have no extracted text.
func (s *SQLiteStore) ListURLsWithoutContent(ctx context.Context, limit int) ([]model.Item, error) {
	if limit <= 0 {
		limit = 50
	}
	q := fmt.Sprintf(`SELECT %s FROM items i WHERE i.type = 'link' AND i.extracted_text = '' ORDER BY i.created_at DESC LIMIT %d`, prefixColumns("i", itemColumns), limit)
	return s.queryItems(ctx, q, nil)
}

// ListTags returns all tags with their usage counts.
func (s *SQLiteStore) ListTags(ctx context.Context) ([]model.Tag, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name, COUNT(it.item_id) AS count
		FROM tags t
		JOIN item_tags it ON it.tag_id = t.id
		GROUP BY t.id, t.name
		ORDER BY t.name`)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var tags []model.Tag
	for rows.Next() {
		var t model.Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Count); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// TagGraph returns the tag co-occurrence graph.
func (s *SQLiteStore) TagGraph(ctx context.Context) (*model.TagGraph, error) {
	nodes, err := s.ListTags(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT t1.name, t2.name, COUNT(DISTINCT a.item_id) AS weight
		FROM item_tags a
		JOIN item_tags b ON a.item_id = b.item_id AND a.tag_id < b.tag_id
		JOIN tags t1 ON t1.id = a.tag_id
		JOIN tags t2 ON t2.id = b.tag_id
		GROUP BY a.tag_id, b.tag_id
		ORDER BY weight DESC`)
	if err != nil {
		return nil, fmt.Errorf("tag graph edges: %w", err)
	}
	defer rows.Close()

	var edges []model.TagEdge
	for rows.Next() {
		var e model.TagEdge
		if err := rows.Scan(&e.TagA, &e.TagB, &e.Weight); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &model.TagGraph{Nodes: nodes, Edges: edges}, nil
}

// RenameTag renames a tag across all items.
func (s *SQLiteStore) RenameTag(ctx context.Context, oldName, newName string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tags SET name = ? WHERE name = ?`, newName, oldName)
	if err != nil {
		return fmt.Errorf("rename tag: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("tag not found: %s", oldName)
	}
	return nil
}

// AddTag adds a tag to an item.
func (s *SQLiteStore) AddTag(ctx context.Context, itemID, tag string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	tagID, err := s.ensureTag(ctx, tx, tag)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO item_tags (item_id, tag_id) VALUES (?, ?)`, itemID, tagID)
	if err != nil {
		return fmt.Errorf("add tag: %w", err)
	}
	return tx.Commit()
}

// RemoveTag removes a tag from an item.
func (s *SQLiteStore) RemoveTag(ctx context.Context, itemID, tag string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM item_tags WHERE item_id = ? AND tag_id = (SELECT id FROM tags WHERE name = ?)`,
		itemID, tag)
	if err != nil {
		return fmt.Errorf("remove tag: %w", err)
	}
	return nil
}

// ListCollections returns all collections.
func (s *SQLiteStore) ListCollections(ctx context.Context) ([]model.Collection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description FROM collections ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	defer rows.Close()

	var cols []model.Collection
	for rows.Next() {
		var c model.Collection
		if err := rows.Scan(&c.ID, &c.Name, &c.Description); err != nil {
			return nil, fmt.Errorf("scan collection: %w", err)
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// ListCollectionsByRecentActivity returns collections ordered by the
// most recent item-add timestamp (newest first). Collections with no
// items fall to the bottom — they have no activity to sort by.
// Backs the Mac sidebar's "Recent" cap-at-N section.
func (s *SQLiteStore) ListCollectionsByRecentActivity(ctx context.Context, limit int) ([]model.Collection, error) {
	q := `
		SELECT c.id, c.name, c.description
		FROM collections c
		LEFT JOIN item_collections ic ON ic.collection_id = c.id
		GROUP BY c.id, c.name, c.description
		ORDER BY MAX(ic.added_at) DESC NULLS LAST, c.name
	`
	args := []any{}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	return s.queryCollections(ctx, q, args)
}

// ListCollectionsByFrequency returns collections ordered by
// view_count DESC. Ties broken by name so the order is stable
// across calls.
func (s *SQLiteStore) ListCollectionsByFrequency(ctx context.Context, limit int) ([]model.Collection, error) {
	q := `
		SELECT id, name, description
		FROM collections
		ORDER BY view_count DESC, name
	`
	args := []any{}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	return s.queryCollections(ctx, q, args)
}

// queryCollections is the shared scan helper for the Sorted-list
// variants above. Kept private; callers go through the typed
// methods so the SQL stays in one file.
func (s *SQLiteStore) queryCollections(ctx context.Context, q string, args []any) ([]model.Collection, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	defer rows.Close()
	var cols []model.Collection
	for rows.Next() {
		var c model.Collection
		if err := rows.Scan(&c.ID, &c.Name, &c.Description); err != nil {
			return nil, fmt.Errorf("scan collection: %w", err)
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// MergeCollections folds the memberships of `others` into `survivor`
// and deletes the merged collections. Items already in survivor stay
// at their existing positions; merged items append at the end in
// their original relative order. INSERT OR IGNORE collapses
// duplicates silently.
//
// Single transaction: a mid-merge crash rolls back, never leaves the
// store with half the items moved and the original collections gone.
func (s *SQLiteStore) MergeCollections(ctx context.Context, survivor string, others []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var survivorID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM collections WHERE name = ?`, survivor,
	).Scan(&survivorID); err != nil {
		return fmt.Errorf("survivor collection %q not found: %w", survivor, err)
	}

	for _, name := range others {
		if name == survivor {
			continue
		}
		var mergedID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM collections WHERE name = ?`, name,
		).Scan(&mergedID); err != nil {
			return fmt.Errorf("collection %q not found: %w", name, err)
		}
		// Start positions for the appended items at the end of
		// the survivor's existing curated order. COALESCE keeps
		// the math right when the survivor is empty.
		var nextPos int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(position), -1) + 1
			 FROM item_collections WHERE collection_id = ?`,
			survivorID,
		).Scan(&nextPos); err != nil {
			return fmt.Errorf("survivor next position: %w", err)
		}
		// Fold rows in original-position order. ROW_NUMBER()
		// preserves relative ordering; INSERT OR IGNORE drops
		// rows where (item_id, survivor_id) already exists.
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO item_collections
				(item_id, collection_id, position, added_at)
			SELECT item_id,
			       ?,
			       ? + (ROW_NUMBER() OVER (ORDER BY position)) - 1,
			       added_at
			FROM item_collections
			WHERE collection_id = ?
		`, survivorID, nextPos, mergedID); err != nil {
			return fmt.Errorf("fold items from %q: %w", name, err)
		}
		// Drop the merged collection. The ON DELETE CASCADE on
		// item_collections.collection_id removes the now-orphan
		// membership rows.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM collections WHERE id = ?`, mergedID,
		); err != nil {
			return fmt.Errorf("delete %q: %w", name, err)
		}
	}

	return tx.Commit()
}

// TouchCollection bumps the view_count column. Called from the Mac
// sidebar when the user navigates to a collection so the "Frequent"
// sort reflects actual usage. Idempotent on missing names — silent
// no-op so a stale sidebar click after rename doesn't error.
func (s *SQLiteStore) TouchCollection(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE collections SET view_count = view_count + 1 WHERE name = ?`,
		name,
	)
	return err
}

// CreateCollection creates a new collection.
func (s *SQLiteStore) CreateCollection(ctx context.Context, name, description string) (*model.Collection, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO collections (name, description) VALUES (?, ?)`, name, description)
	if err != nil {
		return nil, fmt.Errorf("create collection: %w", err)
	}
	id, _ := res.LastInsertId()
	return &model.Collection{ID: id, Name: name, Description: description}, nil
}

// GetCollection fetches a collection by name.
func (s *SQLiteStore) GetCollection(ctx context.Context, name string) (*model.Collection, error) {
	var c model.Collection
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description FROM collections WHERE name = ?`, name).
		Scan(&c.ID, &c.Name, &c.Description)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("collection not found: %s", name)
	}
	if err != nil {
		return nil, fmt.Errorf("get collection: %w", err)
	}
	return &c, nil
}

// DeleteCollection removes a collection (not the items in it).
func (s *SQLiteStore) DeleteCollection(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM collections WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete collection: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("collection not found: %s", name)
	}
	return nil
}

// AddToCollection adds an item to a collection.
func (s *SQLiteStore) AddToCollection(ctx context.Context, itemID, collectionName string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := s.addToCollectionTx(ctx, tx, itemID, collectionName); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveFromCollection removes an item from a collection.
func (s *SQLiteStore) RemoveFromCollection(ctx context.Context, itemID, collectionName string) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM item_collections WHERE item_id = ?
		AND collection_id = (SELECT id FROM collections WHERE name = ?)`,
		itemID, collectionName)
	if err != nil {
		return fmt.Errorf("remove from collection: %w", err)
	}
	return nil
}

// ListCollectionItems returns items in a collection.
func (s *SQLiteStore) ListCollectionItems(ctx context.Context, name string, filter model.ItemFilter) ([]model.Item, error) {
	filter.Collection = name
	return s.ListItems(ctx, filter)
}

// LinkItems creates a link between two items.
func (s *SQLiteStore) LinkItems(ctx context.Context, fromID, toID, label string, directed bool) error {
	if fromID == toID {
		return fmt.Errorf("cannot link an item to itself")
	}
	// For undirected links, canonicalize order so lookups are consistent.
	if !directed && fromID > toID {
		fromID, toID = toID, fromID
	}
	dirInt := 0
	if directed {
		dirInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO item_links (item_id_from, item_id_to, label, directed) VALUES (?, ?, ?, ?)`,
		fromID, toID, label, dirInt)
	if err != nil {
		return fmt.Errorf("link items: %w", err)
	}
	return nil
}

// UnlinkItems removes a link between two items.
func (s *SQLiteStore) UnlinkItems(ctx context.Context, idA, idB string) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM item_links WHERE (item_id_from = ? AND item_id_to = ?) OR (item_id_from = ? AND item_id_to = ?)`,
		idA, idB, idB, idA)
	if err != nil {
		return fmt.Errorf("unlink items: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no link found between %s and %s", idA, idB)
	}
	return nil
}

// ListLinks returns all links for an item.
func (s *SQLiteStore) ListLinks(ctx context.Context, itemID string) ([]model.Link, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.title, i.type, l.label,
		       CASE WHEN l.directed = 0 THEN 'none' ELSE 'outgoing' END AS direction
		FROM item_links l JOIN items i ON i.id = l.item_id_to
		WHERE l.item_id_from = ?
		UNION ALL
		SELECT i.id, i.title, i.type, l.label,
		       CASE WHEN l.directed = 0 THEN 'none' ELSE 'incoming' END AS direction
		FROM item_links l JOIN items i ON i.id = l.item_id_from
		WHERE l.item_id_to = ?
		ORDER BY title`, itemID, itemID)
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	defer rows.Close()

	var links []model.Link
	for rows.Next() {
		var lk model.Link
		if err := rows.Scan(&lk.ItemID, &lk.Title, &lk.Type, &lk.Label, &lk.Direction); err != nil {
			return nil, fmt.Errorf("scan link: %w", err)
		}
		links = append(links, lk)
	}
	return links, rows.Err()
}

// --- internal helpers ---

func (s *SQLiteStore) scanItem(row *sql.Row) (*model.Item, error) {
	var item model.Item
	var meta string
	var archived int
	var lat, lon sql.NullFloat64
	var locSrc sql.NullString
	var capturedAt sql.NullTime
	var chatHistoryStr string
	var speakerMapStr string
	err := row.Scan(
		&item.ID, &item.Type, &item.Title, &item.URL, &item.Notes,
		&item.SourcePath, &item.StorePath, &item.ContentHash, &item.ExtractedText,
		&item.MimeType, &item.FileSize, &meta, &item.CreatedAt, &item.UpdatedAt,
		&archived, &item.ThumbnailPath,
		&lat, &lon, &locSrc,
		&capturedAt, &chatHistoryStr,
		&item.Caption,
		&speakerMapStr,
	)
	if err != nil {
		return nil, fmt.Errorf("scan item [TRACE-99-SINGULAR]: %w", err)
	}
	item.Metadata = json.RawMessage(meta)
	item.Archived = archived != 0
	item.Location = buildLocation(lat, lon, locSrc)
	item.CapturedAt = nullTimeToPtr(capturedAt)
	if chatHistoryStr != "" {
		_ = json.Unmarshal([]byte(chatHistoryStr), &item.ChatHistory)
	}
	if speakerMapStr != "" {
		_ = json.Unmarshal([]byte(speakerMapStr), &item.SpeakerMap)
	}
	return &item, nil
}

func (s *SQLiteStore) scanItems(rows *sql.Rows) ([]model.Item, error) {
	var items []model.Item
	for rows.Next() {
		var item model.Item
		var meta string
		var archived int
		var lat, lon sql.NullFloat64
		var locSrc sql.NullString
		var capturedAt sql.NullTime
		var chatHistoryStr string
		var speakerMapStr string
		err := rows.Scan(
			&item.ID, &item.Type, &item.Title, &item.URL, &item.Notes,
			&item.SourcePath, &item.StorePath, &item.ContentHash, &item.ExtractedText,
			&item.MimeType, &item.FileSize, &meta, &item.CreatedAt, &item.UpdatedAt,
			&archived, &item.ThumbnailPath,
			&lat, &lon, &locSrc,
			&capturedAt, &chatHistoryStr,
			&item.Caption,
			&speakerMapStr,
		)
		if err != nil {
			return nil, fmt.Errorf("scan item [TRACE-99]: %w", err)
		}
		item.Metadata = json.RawMessage(meta)
		item.Archived = archived != 0
		item.Location = buildLocation(lat, lon, locSrc)
		item.CapturedAt = nullTimeToPtr(capturedAt)
		if chatHistoryStr != "" {
			_ = json.Unmarshal([]byte(chatHistoryStr), &item.ChatHistory)
		}
		if speakerMapStr != "" {
			_ = json.Unmarshal([]byte(speakerMapStr), &item.SpeakerMap)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// nullTimeToPtr lifts a sql.NullTime into the *time.Time the model
// uses for CapturedAt. NULL in the DB → nil pointer → JSON encoder
// omits the field. Mirrors buildLocation's role for the optional
// Location field.
func nullTimeToPtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// ptrToNullTime is the inverse — for INSERT/UPDATE binding.
func ptrToNullTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

// splitLocation flattens a *Location into the three SQL parameters
// the items table stores. nil location maps to (NULL, NULL, "").
func splitLocation(loc *model.Location) (lat, lon sql.NullFloat64, src string) {
	if loc == nil {
		return sql.NullFloat64{}, sql.NullFloat64{}, ""
	}
	return sql.NullFloat64{Float64: loc.Lat, Valid: true},
		sql.NullFloat64{Float64: loc.Lon, Valid: true},
		loc.Source
}

// buildLocation is the inverse — nil latitude (NULL in SQL) is the
// sentinel for "no location" so the item gets a nil Location pointer
// and the JSON encoder omits the field entirely.
func buildLocation(lat, lon sql.NullFloat64, src sql.NullString) *model.Location {
	if !lat.Valid {
		return nil
	}
	return &model.Location{
		Lat:    lat.Float64,
		Lon:    lon.Float64,
		Source: src.String,
	}
}

func prefixColumns(prefix, cols string) string {
	parts := strings.Split(cols, ", ")
	for i, p := range parts {
		parts[i] = prefix + "." + p
	}
	return strings.Join(parts, ", ")
}

func (s *SQLiteStore) queryItems(ctx context.Context, q string, args []any) ([]model.Item, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query items: %w", err)
	}
	defer rows.Close()

	items, err := s.scanItems(rows)
	if err != nil {
		return nil, err
	}

	for i := range items {
		if err := s.loadRelations(ctx, &items[i]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *SQLiteStore) loadRelations(ctx context.Context, item *model.Item) error {
	// Load tags
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name FROM tags t
		JOIN item_tags it ON it.tag_id = t.id
		WHERE it.item_id = ? ORDER BY t.name`, item.ID)
	if err != nil {
		return fmt.Errorf("load tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t model.Tag
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			return fmt.Errorf("scan tag: %w", err)
		}
		item.Tags = append(item.Tags, t)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Load collections
	rows2, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.description FROM collections c
		JOIN item_collections ic ON ic.collection_id = c.id
		WHERE ic.item_id = ? ORDER BY c.name`, item.ID)
	if err != nil {
		return fmt.Errorf("load collections: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var c model.Collection
		if err := rows2.Scan(&c.ID, &c.Name, &c.Description); err != nil {
			return fmt.Errorf("scan collection: %w", err)
		}
		item.Collections = append(item.Collections, c)
	}
	if err := rows2.Err(); err != nil {
		return err
	}

	// Load links
	links, err := s.ListLinks(ctx, item.ID)
	if err != nil {
		return err
	}
	item.Links = links

	// Load attached files (item_files sidecar). Items with no
	// attachments get nil/empty here — JSON omitempty hides the
	// field entirely so existing consumers don't see `"files": []`
	// noise on every single-file item.
	files, err := s.ListItemFiles(ctx, item.ID)
	if err != nil {
		return fmt.Errorf("load item_files: %w", err)
	}
	if len(files) > 0 {
		item.Files = files
	}
	return nil
}

func (s *SQLiteStore) buildListQuery(filter model.ItemFilter) (string, []any) {
	var where []string
	var args []any

	if filter.Type != "" {
		if filter.Type == "audio" {
			where = append(where, "(i.type = 'file' AND i.mime_type LIKE 'audio/%')")
		} else {
			where = append(where, "i.type = ?")
			args = append(args, filter.Type)
		}
	}
	if filter.Untagged {
		// Untagged short-circuits any include/exclude tag filters —
		// those are meaningless on items with zero tags.
		where = append(where, "NOT EXISTS (SELECT 1 FROM item_tags it WHERE it.item_id = i.id)")
	} else {
		if len(filter.Tags) > 0 {
			placeholders := make([]string, len(filter.Tags))
			for i, t := range filter.Tags {
				placeholders[i] = "?"
				args = append(args, t)
			}
			where = append(where, fmt.Sprintf(
				"i.id IN (SELECT it.item_id FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE t.name IN (%s))",
				strings.Join(placeholders, ","),
			))
		}
		if len(filter.ExcludeTags) > 0 {
			placeholders := make([]string, len(filter.ExcludeTags))
			for i, t := range filter.ExcludeTags {
				placeholders[i] = "?"
				args = append(args, t)
			}
			where = append(where, fmt.Sprintf(
				"i.id NOT IN (SELECT it.item_id FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE t.name IN (%s))",
				strings.Join(placeholders, ","),
			))
		}
	}
	hasCollection := filter.Collection != ""
	if hasCollection {
		// JOIN (rather than IN subquery) so ORDER BY can reference
		// `ic.position` for curated ordering.
		where = append(where, "c.name = ?")
		args = append(args, filter.Collection)
	}
	if filter.LinkedTo != "" {
		where = append(where, `i.id IN (
			SELECT item_id_to FROM item_links WHERE item_id_from = ?
			UNION
			SELECT item_id_from FROM item_links WHERE item_id_to = ?
		)`)
		args = append(args, filter.LinkedTo, filter.LinkedTo)
	}
	if filter.After != nil {
		where = append(where, "i.created_at >= ?")
		args = append(args, *filter.After)
	}
	if filter.Before != nil {
		where = append(where, "i.created_at <= ?")
		args = append(args, *filter.Before)
	}
	if t, ok := resolveRecent(filter.Recent); ok {
		where = append(where, "i.created_at >= ?")
		args = append(args, t)
	}
	if filter.OnlyArchived {
		where = append(where, "i.archived = 1")
	} else if !filter.IncludeArchived {
		where = append(where, "i.archived = 0")
	}

	q := fmt.Sprintf("SELECT %s FROM items i", prefixColumns("i", itemColumns))
	if hasCollection {
		q += " JOIN item_collections ic ON ic.item_id = i.id" +
			" JOIN collections c ON c.id = ic.collection_id"
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	// Curated order within a collection (drag-to-reorder semantics);
	// chronological newest-first everywhere else.
	if hasCollection {
		q += " ORDER BY ic.position ASC, i.created_at DESC"
	} else {
		q += " ORDER BY i.created_at DESC"
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	if filter.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	return q, args
}

func (s *SQLiteStore) setTags(ctx context.Context, tx *sql.Tx, itemID string, tags []model.Tag) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM item_tags WHERE item_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("clear tags: %w", err)
	}
	for _, t := range tags {
		tagID, err := s.ensureTag(ctx, tx, t.Name)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO item_tags (item_id, tag_id) VALUES (?, ?)`, itemID, tagID)
		if err != nil {
			return fmt.Errorf("set tag: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) ensureTag(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tags (name) VALUES (?)`, name)
	if err != nil {
		return 0, fmt.Errorf("ensure tag: %w", err)
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM tags WHERE name = ?`, name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get tag id: %w", err)
	}
	return id, nil
}

func (s *SQLiteStore) ensureCollection(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO collections (name) VALUES (?)`, name)
	if err != nil {
		return 0, fmt.Errorf("ensure collection: %w", err)
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM collections WHERE name = ?`, name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get collection id: %w", err)
	}
	return id, nil
}

func (s *SQLiteStore) addToCollectionTx(ctx context.Context, tx *sql.Tx, itemID, collectionName string) error {
	colID, err := s.ensureCollection(ctx, tx, collectionName)
	if err != nil {
		return err
	}
	// New items go to the end of the collection's curated order.
	// COALESCE keeps the first item at position 0.
	var nextPos int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(position), -1) + 1 FROM item_collections WHERE collection_id = ?`,
		colID,
	).Scan(&nextPos); err != nil {
		return fmt.Errorf("query next position: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		// added_at = CURRENT_TIMESTAMP records when this membership
		// began. Used by the Mac sidebar's "Recent" sort mode to
		// surface the Collections you're actively building. INSERT
		// OR IGNORE means duplicate adds are silent — but in that
		// case we don't bump added_at, which matches user
		// expectation (re-adding the same item shouldn't "freshen"
		// the collection).
		`INSERT OR IGNORE INTO item_collections (item_id, collection_id, position, added_at)
		 VALUES (?, ?, ?, CURRENT_TIMESTAMP)`,
		itemID, colID, nextPos,
	)
	if err != nil {
		return fmt.Errorf("add to collection: %w", err)
	}
	return nil
}

// ReorderCollection sets explicit positions for every item in
// `orderedIDs` within the named collection. Items not in the list
// keep their current positions (unchanged). Caller is responsible
// for passing the full desired order — partial reorders should
// pass everything, with unmoved items in their current spots.
func (s *SQLiteStore) ReorderCollection(ctx context.Context, collectionName string, orderedIDs []string) error {
	if collectionName == "" {
		return fmt.Errorf("collection name required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var colID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM collections WHERE name = ?`, collectionName,
	).Scan(&colID); err != nil {
		return fmt.Errorf("collection not found: %s", collectionName)
	}

	for idx, id := range orderedIDs {
		if id == "" {
			continue
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE item_collections SET position = ? WHERE collection_id = ? AND item_id = ?`,
			idx, colID, id,
		)
		if err != nil {
			return fmt.Errorf("reorder %s: %w", id, err)
		}
	}
	return tx.Commit()
}

// sortPair returns IDs in consistent sorted order for dedup lookups.
func sortPair(a, b string) (string, string) {
	if a > b {
		return b, a
	}
	return a, b
}

// DismissDupePair marks a pair of items as reviewed (not duplicates).
func (s *SQLiteStore) DismissDupePair(ctx context.Context, idA, idB string) error {
	a, b := sortPair(idA, idB)
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO dismissed_dupes (item_id_a, item_id_b) VALUES (?, ?)`, a, b)
	if err != nil {
		return fmt.Errorf("dismiss dupe pair: %w", err)
	}
	return nil
}

// IsDupeDismissed checks if a pair has been dismissed.
func (s *SQLiteStore) IsDupeDismissed(ctx context.Context, idA, idB string) bool {
	a, b := sortPair(idA, idB)
	var count int
	s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dismissed_dupes WHERE item_id_a = ? AND item_id_b = ?`, a, b).Scan(&count)
	return count > 0
}

// ListDismissedPairs returns all dismissed pairs.
func (s *SQLiteStore) ListDismissedPairs(ctx context.Context) ([][2]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id_a, item_id_b FROM dismissed_dupes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pairs [][2]string
	for rows.Next() {
		var a, b string
		rows.Scan(&a, &b)
		pairs = append(pairs, [2]string{a, b})
	}
	return pairs, rows.Err()
}

// DismissMoment records the cluster signature as user-rejected.
// Idempotent — re-dismissing refreshes the timestamp + sample
// title but doesn't fail. Tiny rows; no GC needed.
func (s *SQLiteStore) DismissMoment(ctx context.Context, signature string, itemCount int, sampleTitle string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO dismissed_moments (signature, item_count, sample_title)
		VALUES (?, ?, ?)
		ON CONFLICT(signature) DO UPDATE SET
			dismissed_at = CURRENT_TIMESTAMP,
			item_count = excluded.item_count,
			sample_title = excluded.sample_title
	`, signature, itemCount, sampleTitle)
	return err
}

// UndismissMoment removes a dismissal so the cluster can re-surface
// the next time its signature matches.
func (s *SQLiteStore) UndismissMoment(ctx context.Context, signature string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM dismissed_moments WHERE signature = ?`,
		signature,
	)
	return err
}

// IsMomentDismissed returns true if the signature has been
// dismissed. Used inline by the suggestion engine to skip
// already-dismissed clusters; for batch lookups, prefer
// DismissedMomentSignatures.
func (s *SQLiteStore) IsMomentDismissed(ctx context.Context, signature string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dismissed_moments WHERE signature = ?`,
		signature,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// DismissedMomentSignatures returns every dismissed signature as a
// hashset for O(1) lookups during a single suggestion-build pass.
func (s *SQLiteStore) DismissedMomentSignatures(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT signature FROM dismissed_moments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var sig string
		if err := rows.Scan(&sig); err != nil {
			return nil, err
		}
		out[sig] = true
	}
	return out, rows.Err()
}

// ListDismissedMoments returns the dismissal table for surfacing in
// a "Show dismissed" view. Newest first so the user sees their most
// recent vetoes when undoing.
func (s *SQLiteStore) ListDismissedMoments(ctx context.Context) ([]model.DismissedMoment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT signature, dismissed_at, item_count, sample_title
		FROM dismissed_moments
		ORDER BY dismissed_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.DismissedMoment
	for rows.Next() {
		var dm model.DismissedMoment
		if err := rows.Scan(&dm.Signature, &dm.DismissedAt, &dm.ItemCount, &dm.SampleTitle); err != nil {
			return nil, err
		}
		out = append(out, dm)
	}
	return out, rows.Err()
}

// SaveSearch persists a named search query and filter. `live` flips
// the sidebar treatment in stash-mac (Smart Collection vs static
// snapshot); the CLI run path is the same regardless.
func (s *SQLiteStore) SaveSearch(ctx context.Context, name, query string, filter model.ItemFilter, live bool) error {
	filterJSON, err := json.Marshal(filter)
	if err != nil {
		return fmt.Errorf("marshal filter: %w", err)
	}
	liveVal := 0
	if live {
		liveVal = 1
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO saved_searches (name, query, filter_json, live) VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET query=excluded.query, filter_json=excluded.filter_json, live=excluded.live`,
		name, query, string(filterJSON), liveVal)
	if err != nil {
		return fmt.Errorf("save search: %w", err)
	}
	return nil
}

// ListSavedSearches returns all saved searches.
func (s *SQLiteStore) ListSavedSearches(ctx context.Context) ([]model.SavedSearch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, query, filter_json, live FROM saved_searches ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list saved searches: %w", err)
	}
	defer rows.Close()

	var searches []model.SavedSearch
	for rows.Next() {
		var ss model.SavedSearch
		var filterJSON string
		var live int
		if err := rows.Scan(&ss.ID, &ss.Name, &ss.Query, &filterJSON, &live); err != nil {
			return nil, fmt.Errorf("scan saved search: %w", err)
		}
		json.Unmarshal([]byte(filterJSON), &ss.Filter)
		ss.Live = live != 0
		searches = append(searches, ss)
	}
	return searches, rows.Err()
}

// GetSavedSearch retrieves a saved search by name.
func (s *SQLiteStore) GetSavedSearch(ctx context.Context, name string) (*model.SavedSearch, error) {
	var ss model.SavedSearch
	var filterJSON string
	var live int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, query, filter_json, live FROM saved_searches WHERE name = ?`, name,
	).Scan(&ss.ID, &ss.Name, &ss.Query, &filterJSON, &live)
	if err != nil {
		return nil, fmt.Errorf("saved search not found: %s", name)
	}
	json.Unmarshal([]byte(filterJSON), &ss.Filter)
	ss.Live = live != 0
	return &ss, nil
}

// DeleteSavedSearch removes a saved search by name.
func (s *SQLiteStore) DeleteSavedSearch(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM saved_searches WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete saved search: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("saved search not found: %s", name)
	}
	return nil
}

// RenameSavedSearch updates a saved search's name in place. Errors out
// if the new name is already in use, the names match (no-op), or the
// old name doesn't exist. The DB row's id stays the same so anything
// keyed on id (none today, but future-proof) keeps pointing at the
// same row.
func (s *SQLiteStore) RenameSavedSearch(ctx context.Context, oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" {
		return fmt.Errorf("old name is required")
	}
	if newName == "" {
		return fmt.Errorf("new name is required")
	}
	if oldName == newName {
		return nil
	}
	// Pre-check the collision so we surface a clean error rather
	// than relying on the UNIQUE constraint to fail out of a tx.
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM saved_searches WHERE name = ?`, newName,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check new name: %w", err)
	}
	if exists > 0 {
		return fmt.Errorf("a saved search named %q already exists", newName)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE saved_searches SET name = ? WHERE name = ?`, newName, oldName,
	)
	if err != nil {
		return fmt.Errorf("rename saved search: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("saved search not found: %s", oldName)
	}
	return nil
}

// RecordSearchClick appends one row to the search-click log. `query`
// is the text in the search field at the moment the user committed
// (clicked a result or pressed Enter). `itemID` is the specific
// item that got the click — may be empty when the commit happened
// without selecting a row.
//
// Empty queries are silently ignored so callers don't have to
// guard. The same query may appear repeatedly; views aggregate
// via GROUP BY at read time.
func (s *SQLiteStore) RecordSearchClick(ctx context.Context, query, itemID string) error {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	var idArg any
	if strings.TrimSpace(itemID) != "" {
		idArg = itemID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO search_click_log (query, item_id, clicked_at)
		VALUES (?, ?, ?)`,
		q, idArg, time.Now().UTC().Unix(),
	)
	return err
}

// SearchHistorySort selects the order returned by
// ListSearchHistory. "recent" → MAX(clicked_at) DESC,
// "frequent" → COUNT(*) DESC.
type SearchHistorySort string

const (
	SearchHistoryRecent   SearchHistorySort = "recent"
	SearchHistoryFrequent SearchHistorySort = "frequent"
)

// ListSearchHistory groups the click log by query and returns
// either the most-recently-used or most-frequent queries.
// Limit ≤ 0 means "all".
func (s *SQLiteStore) ListSearchHistory(ctx context.Context, sortBy SearchHistorySort, limit int) ([]model.SearchHistoryEntry, error) {
	var orderBy string
	switch sortBy {
	case SearchHistoryFrequent:
		// Frequent: highest count first; ties go to most-recently-
		// used so a query you used yesterday outranks one you used
		// a year ago when both have the same count.
		orderBy = "count DESC, last_used_at DESC"
	default:
		orderBy = "last_used_at DESC"
	}
	query := `
		SELECT query, COUNT(*) AS count, MAX(clicked_at) AS last_used_at
		FROM search_click_log
		GROUP BY query
		ORDER BY ` + orderBy
	var args []any
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SearchHistoryEntry
	for rows.Next() {
		var e model.SearchHistoryEntry
		var ts int64
		if err := rows.Scan(&e.Query, &e.Count, &ts); err != nil {
			return nil, err
		}
		e.LastUsedAt = time.Unix(ts, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClearSearchHistory drops the entire click log. Used by the
// Settings "clear history" button (future) and tests.
func (s *SQLiteStore) ClearSearchHistory(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM search_click_log`)
	return err
}

// DeleteSearchHistoryEntry removes all log rows for one query.
// Used when the user dismisses a row in the Recent / Frequent view.
func (s *SQLiteStore) DeleteSearchHistoryEntry(ctx context.Context, query string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM search_click_log WHERE query = ?`, query)
	return err
}

// Stats returns aggregate statistics about the stash.
func (s *SQLiteStore) Stats(ctx context.Context) (*model.StashStats, error) {
	st := &model.StashStats{
		TypeCounts: make(map[string]int),
	}

	// Total items and size
	s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(file_size),0) FROM items`).Scan(&st.TotalItems, &st.TotalSize)

	// Counts by type
	rows, err := s.db.QueryContext(ctx, `SELECT type, COUNT(*) FROM items GROUP BY type`)
	if err != nil {
		return nil, fmt.Errorf("type counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		var c int
		rows.Scan(&t, &c)
		display := model.ItemType(t).Display()
		st.TypeCounts[display] = c
	}

	// Tag count
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tags`).Scan(&st.TagCount)

	// Collection count
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM collections`).Scan(&st.CollCount)

	// Link count
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_links`).Scan(&st.LinkCount)

	// Top tags (by usage)
	tagRows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name, COUNT(it.item_id) AS count
		FROM tags t
		JOIN item_tags it ON it.tag_id = t.id
		GROUP BY t.id, t.name
		ORDER BY count DESC
		LIMIT 10`)
	if err != nil {
		return nil, fmt.Errorf("top tags: %w", err)
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var tag model.Tag
		tagRows.Scan(&tag.ID, &tag.Name, &tag.Count)
		st.TopTags = append(st.TopTags, tag)
	}

	// Oldest and newest
	var oldestStr, newestStr sql.NullString
	s.db.QueryRowContext(ctx, `SELECT MIN(created_at) FROM items`).Scan(&oldestStr)
	s.db.QueryRowContext(ctx, `SELECT MAX(created_at) FROM items`).Scan(&newestStr)
	for _, pair := range []struct {
		str  sql.NullString
		dest **time.Time
	}{
		{oldestStr, &st.OldestItem},
		{newestStr, &st.NewestItem},
	} {
		if pair.str.Valid && pair.str.String != "" {
			for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z"} {
				if t, err := time.Parse(layout, pair.str.String); err == nil {
					*pair.dest = &t
					break
				}
			}
		}
	}

	// Growth by month (last 12 months)
	monthRows, err := s.db.QueryContext(ctx, `
		SELECT strftime('%Y-%m', created_at) AS month, COUNT(*)
		FROM items
		GROUP BY month
		ORDER BY month DESC
		LIMIT 12`)
	if err != nil {
		return nil, fmt.Errorf("month counts: %w", err)
	}
	defer monthRows.Close()
	for monthRows.Next() {
		var mc model.MonthCount
		monthRows.Scan(&mc.Month, &mc.Count)
		st.MonthCounts = append(st.MonthCounts, mc)
	}

	return st, nil
}

func (s *SQLiteStore) RebuildFTS(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO items_fts(items_fts) VALUES('rebuild')")
	return err
}

func (s *SQLiteStore) AllReferencedHashes(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT content_hash FROM items WHERE content_hash != ''
		UNION
		SELECT content_hash FROM item_files WHERE content_hash != ''
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}
	return hashes, rows.Err()
}

func (s *SQLiteStore) DeleteOrphanedFiles(ctx context.Context) (int, error) {
	// Identification of orphans requires the FileStore which is not owned
	// by the Store struct. The logic should live in internal/stash
	// or the cmd layer where both are available.
	return 0, fmt.Errorf("DeleteOrphanedFiles not implemented in store layer")
}

func marshalMeta(data json.RawMessage) (string, error) {
	if len(data) == 0 {
		return "{}", nil
	}
	return string(data), nil
}

// applyRegexFilter narrows `items` to those whose title + notes + URL +
// extracted text matches the RE2 pattern. A leading `!` negates the
// match. Empty pattern is a no-op. An invalid pattern returns the
// items unchanged with no error — Smart Collections shouldn't fail
// silently to no rows when the user fat-fingers a regex, and there's
// no UI surface to bubble a parse error to from this layer.
func applyRegexFilter(items []model.Item, pattern string) ([]model.Item, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return items, nil
	}
	negate := false
	if strings.HasPrefix(pattern, "!") {
		negate = true
		pattern = strings.TrimSpace(pattern[1:])
		if pattern == "" {
			return items, nil
		}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return items, nil
	}
	out := make([]model.Item, 0, len(items))
	for _, it := range items {
		hay := it.Title + "\n" + it.Notes + "\n" + it.URL + "\n" + it.ExtractedText
		matched := re.MatchString(hay)
		if matched != negate {
			out = append(out, it)
		}
	}
	return out, nil
}

// resolveRecent translates a relative spec like "7d" / "2w" / "1h" into
// the absolute timestamp `now - duration`. Returns (zero, false) for
// empty or unparseable specs so callers can skip the WHERE clause.
//
// Smart Collections store the spec verbatim and re-resolve it on every
// query, which is the whole point — "Recent Captures" should always
// mean "captures in the last 7 days from *today*", not from the day
// the search was saved. `time.ParseDuration` doesn't understand `d`
// or `w`, so we extend it manually.
func resolveRecent(spec string) (time.Time, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return time.Time{}, false
	}
	// Strip a `d` or `w` suffix, multiply into hours, hand to ParseDuration.
	if n := len(spec); n > 1 {
		last := spec[n-1]
		if last == 'd' || last == 'w' {
			rest := spec[:n-1]
			val, err := time.ParseDuration(rest + "h")
			if err != nil {
				return time.Time{}, false
			}
			mult := time.Duration(24)
			if last == 'w' {
				mult = 24 * 7
			}
			return time.Now().Add(-val * mult).UTC(), true
		}
	}
	d, err := time.ParseDuration(spec)
	if err != nil {
		return time.Time{}, false
	}
	return time.Now().Add(-d).UTC(), true
}
