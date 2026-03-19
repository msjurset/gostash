package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
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
	return s.queryItems(ctx, q, args)
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

	q := "SELECT i.* FROM items i WHERE " + strings.Join(where, " AND ") + " ORDER BY i.created_at DESC"

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	if filter.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	return s.queryItems(ctx, q, args)
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

// ListTags returns all tags with their usage counts.
func (s *SQLiteStore) ListTags(ctx context.Context) ([]model.Tag, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name FROM tags t
		WHERE EXISTS (SELECT 1 FROM item_tags it WHERE it.tag_id = t.id)
		ORDER BY t.name`)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var tags []model.Tag
	for rows.Next() {
		var t model.Tag
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
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
	err := row.Scan(
		&item.ID, &item.Type, &item.Title, &item.URL, &item.Notes,
		&item.SourcePath, &item.StorePath, &item.ContentHash, &item.ExtractedText,
		&item.MimeType, &item.FileSize, &meta, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	item.Metadata = json.RawMessage(meta)
	return &item, nil
}

func (s *SQLiteStore) scanItems(rows *sql.Rows) ([]model.Item, error) {
	var items []model.Item
	for rows.Next() {
		var item model.Item
		var meta string
		err := rows.Scan(
			&item.ID, &item.Type, &item.Title, &item.URL, &item.Notes,
			&item.SourcePath, &item.StorePath, &item.ContentHash, &item.ExtractedText,
			&item.MimeType, &item.FileSize, &meta, &item.CreatedAt, &item.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan item: %w", err)
		}
		item.Metadata = json.RawMessage(meta)
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

func marshalMeta(data json.RawMessage) (string, error) {
	if len(data) == 0 {
		return "{}", nil
	}
	return string(data), nil
}
