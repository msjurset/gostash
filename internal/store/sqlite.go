package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/model"

	_ "modernc.org/sqlite"
)

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

	// Enable WAL mode and foreign keys
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
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

	return nil
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
func (s *SQLiteStore) Checkpoint() error {
	_, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// prefixQuery converts a plain search string into an FTS5 prefix query
// so that partial words match. "hello wor" becomes "hello* wor*".
// Words that already end with * are left as-is.
func prefixQuery(q string) string {
	words := strings.Fields(q)
	for i, w := range words {
		if !strings.HasSuffix(w, "*") {
			words[i] = w + "*"
		}
	}
	return strings.Join(words, " ")
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

	_, err = tx.ExecContext(ctx, `
		INSERT INTO items (id, type, title, url, notes, source_path, store_path,
			content_hash, extracted_text, mime_type, file_size, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.Type, item.Title, item.URL, item.Notes, item.SourcePath,
		item.StorePath, item.ContentHash, item.ExtractedText, item.MimeType,
		item.FileSize, meta, item.CreatedAt, item.UpdatedAt,
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
	row := s.db.QueryRowContext(ctx, `SELECT * FROM items WHERE id = ?`, id)
	item, err := s.scanItem(row)
	if err == sql.ErrNoRows && len(id) >= 6 {
		row = s.db.QueryRowContext(ctx, `SELECT * FROM items WHERE id LIKE ?`, id+"%")
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
func (s *SQLiteStore) ListItems(ctx context.Context, filter model.ItemFilter) ([]model.Item, error) {
	q, args := s.buildListQuery(filter)
	items, err := s.queryItems(ctx, q, args)
	if err != nil {
		return nil, err
	}
	return applyRegexFilter(items, filter.Regex)
}

// SearchItems performs full-text search using FTS5.
func (s *SQLiteStore) SearchItems(ctx context.Context, filter model.ItemFilter) ([]model.Item, error) {
	if filter.Query == "" {
		return s.ListItems(ctx, filter)
	}

	var where []string
	var args []any

	// Build FTS5 query with prefix wildcards so partial words match.
	// Also match items whose tags contain any of the search words.
	ftsQuery := prefixQuery(filter.Query)
	words := strings.Fields(filter.Query)
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
		where = append(where, "i.type = ?")
		args = append(args, filter.Type)
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

	q := "SELECT i.* FROM items i WHERE " + strings.Join(where, " AND ") + " ORDER BY i.created_at DESC"

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	if filter.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	items, err := s.queryItems(ctx, q, args)
	if err != nil {
		return nil, err
	}
	return applyRegexFilter(items, filter.Regex)
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

	res, err := tx.ExecContext(ctx, `
		UPDATE items SET type=?, title=?, url=?, notes=?, source_path=?, store_path=?,
			content_hash=?, extracted_text=?, mime_type=?, file_size=?, metadata=?, updated_at=?
		WHERE id=?`,
		item.Type, item.Title, item.URL, item.Notes, item.SourcePath, item.StorePath,
		item.ContentHash, item.ExtractedText, item.MimeType, item.FileSize,
		meta, item.UpdatedAt, item.ID,
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
	row := s.db.QueryRowContext(ctx, `SELECT * FROM items WHERE url = ? LIMIT 1`, url)
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
		`SELECT * FROM items WHERE content_hash = ? AND archived = 0 LIMIT 1`, hash)
	item, err := s.scanItem(row)
	if err != nil {
		return nil, err
	}
	if err := s.loadRelations(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

// ListURLsWithoutContent returns URL items that have no extracted text.
func (s *SQLiteStore) ListURLsWithoutContent(ctx context.Context, limit int) ([]model.Item, error) {
	if limit <= 0 {
		limit = 50
	}
	q := fmt.Sprintf(`SELECT i.* FROM items i WHERE i.type = 'link' AND i.extracted_text = '' ORDER BY i.created_at DESC LIMIT %d`, limit)
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
	err := row.Scan(
		&item.ID, &item.Type, &item.Title, &item.URL, &item.Notes,
		&item.SourcePath, &item.StorePath, &item.ContentHash, &item.ExtractedText,
		&item.MimeType, &item.FileSize, &meta, &item.CreatedAt, &item.UpdatedAt,
		&archived,
	)
	if err != nil {
		return nil, err
	}
	item.Metadata = json.RawMessage(meta)
	item.Archived = archived != 0
	return &item, nil
}

func (s *SQLiteStore) scanItems(rows *sql.Rows) ([]model.Item, error) {
	var items []model.Item
	for rows.Next() {
		var item model.Item
		var meta string
		var archived int
		err := rows.Scan(
			&item.ID, &item.Type, &item.Title, &item.URL, &item.Notes,
			&item.SourcePath, &item.StorePath, &item.ContentHash, &item.ExtractedText,
			&item.MimeType, &item.FileSize, &meta, &item.CreatedAt, &item.UpdatedAt,
			&archived,
		)
		if err != nil {
			return nil, fmt.Errorf("scan item: %w", err)
		}
		item.Metadata = json.RawMessage(meta)
		item.Archived = archived != 0
		items = append(items, item)
	}
	return items, rows.Err()
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
	return nil
}

func (s *SQLiteStore) buildListQuery(filter model.ItemFilter) (string, []any) {
	var where []string
	var args []any

	if filter.Type != "" {
		where = append(where, "i.type = ?")
		args = append(args, filter.Type)
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
	if filter.Collection != "" {
		where = append(where, "i.id IN (SELECT ic.item_id FROM item_collections ic JOIN collections c ON c.id = ic.collection_id WHERE c.name = ?)")
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

	q := "SELECT i.* FROM items i"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY i.created_at DESC"

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

func (s *SQLiteStore) addToCollectionTx(ctx context.Context, tx *sql.Tx, itemID, collectionName string) error {
	var colID int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM collections WHERE name = ?`, collectionName).Scan(&colID)
	if err != nil {
		return fmt.Errorf("collection not found: %s", collectionName)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO item_collections (item_id, collection_id) VALUES (?, ?)`, itemID, colID)
	if err != nil {
		return fmt.Errorf("add to collection: %w", err)
	}
	return nil
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
