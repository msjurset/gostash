package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/model"
)

// AttachItemFile inserts a new row into item_files. Position
// defaults to the end of the existing row sequence if the caller
// didn't set one explicitly. Caller is responsible for confirming
// the blob (content_hash) exists in the FileStore.
func (s *SQLiteStore) AttachItemFile(ctx context.Context, file *model.ItemFile) error {
	if file.ItemID == "" {
		return fmt.Errorf("item_id required")
	}
	if file.ContentHash == "" {
		return fmt.Errorf("content_hash required")
	}
	if file.StorePath == "" {
		file.StorePath = file.ContentHash
	}
	if file.CreatedAt.IsZero() {
		file.CreatedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	// Default position = max(position) + 1 within this item so the
	// new file lands at the end of the carousel.
	if file.Position == 0 {
		var maxPos sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(position) FROM item_files WHERE item_id = ?`, file.ItemID).
			Scan(&maxPos); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("max position: %w", err)
		}
		if maxPos.Valid {
			file.Position = int(maxPos.Int64) + 1
		}
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO item_files (item_id, store_path, content_hash,
			mime_type, file_size, caption, position, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, file.ItemID, file.StorePath, file.ContentHash, file.MimeType,
		file.FileSize, file.Caption, file.Position, file.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert item_file: %w", err)
	}
	id, _ := res.LastInsertId()
	file.ID = id
	return tx.Commit()
}

// DetachItemFile removes a single row. The blob in the content-
// addressed filestore is intentionally not deleted here — refcount-
// based GC is a separate operation (see `stash check` / future
// `stash gc` work).
func (s *SQLiteStore) DetachItemFile(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM item_files WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete item file: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("item file not found: %d", id)
	}
	return nil
}

// UpdateItemFileCaption updates the caption of an attached file.
func (s *SQLiteStore) UpdateItemFileCaption(ctx context.Context, fileID int64, caption string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE item_files SET caption = ? WHERE id = ?`, caption, fileID)
	if err != nil {
		return fmt.Errorf("update item file caption: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("item file not found: %d", fileID)
	}
	return nil
}

// ListItemFiles returns the attached files for an item ordered by
// position. Returns an empty slice (not nil) for items with no
// attached files so JSON consumers see `"files": []` rather than
// `null`.
func (s *SQLiteStore) ListItemFiles(ctx context.Context, itemID string) ([]model.ItemFile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, item_id, store_path, content_hash, mime_type,
			file_size, caption, position, created_at
		FROM item_files
		WHERE item_id = ?
		ORDER BY position ASC, id ASC
	`, itemID)
	if err != nil {
		return nil, fmt.Errorf("list item_files: %w", err)
	}
	defer rows.Close()
	files := []model.ItemFile{}
	for rows.Next() {
		var f model.ItemFile
		if err := rows.Scan(&f.ID, &f.ItemID, &f.StorePath, &f.ContentHash,
			&f.MimeType, &f.FileSize, &f.Caption, &f.Position, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan item_file: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// ReorderItemFiles rewrites the position column for every row
// belonging to itemID in the order given. orderedIDs must contain
// exactly the set of attached file IDs for the item — partial /
// extraneous IDs return an error.
func (s *SQLiteStore) ReorderItemFiles(ctx context.Context, itemID string, orderedIDs []int64) error {
	// Validate against current rows BEFORE opening a transaction —
	// SQLite ':memory:' tests use a connection-local DB so reading
	// via s.db from inside a tx-bound goroutine sees a different
	// database. Pre-validating also surfaces input errors without
	// the overhead of opening a tx that will only be rolled back.
	current, err := s.ListItemFiles(ctx, itemID)
	if err != nil {
		return err
	}
	have := make(map[int64]bool, len(current))
	for _, f := range current {
		have[f.ID] = true
	}
	if len(orderedIDs) != len(current) {
		return fmt.Errorf("reorder: expected %d ids, got %d", len(current), len(orderedIDs))
	}
	for _, id := range orderedIDs {
		if !have[id] {
			return fmt.Errorf("reorder: file %d not attached to item %s", id, itemID)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	for pos, id := range orderedIDs {
		if _, err := tx.ExecContext(ctx,
			`UPDATE item_files SET position = ? WHERE id = ? AND item_id = ?`,
			pos, id, itemID); err != nil {
			return fmt.Errorf("update position: %w", err)
		}
	}
	return tx.Commit()
}

// PromoteItemFile swaps the given attached file with the item's
// primary (items.store_path). The previous primary is added to the
// item_files table at position 0 so it's not lost.
func (s *SQLiteStore) PromoteItemFile(ctx context.Context, fileID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	var (
		itemID         string
		newHash        string
		newStorePath   string
		newMime        string
		newSize        int64
	)
	if err := tx.QueryRowContext(ctx, `
		SELECT item_id, content_hash, store_path, mime_type, file_size
		FROM item_files WHERE id = ?
	`, fileID).Scan(&itemID, &newHash, &newStorePath, &newMime, &newSize); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("item_file %d not found", fileID)
		}
		return fmt.Errorf("read item_file: %w", err)
	}

	var (
		oldHash      string
		oldStorePath string
		oldMime      string
		oldSize      int64
	)
	if err := tx.QueryRowContext(ctx, `
		SELECT content_hash, store_path, mime_type, file_size
		FROM items WHERE id = ?
	`, itemID).Scan(&oldHash, &oldStorePath, &oldMime, &oldSize); err != nil {
		return fmt.Errorf("read item: %w", err)
	}

	// Promote: swap items.store_path + replace the row that held
	// the newly-primary file with a row holding the demoted primary.
	if _, err := tx.ExecContext(ctx, `
		UPDATE items SET content_hash = ?, store_path = ?, mime_type = ?, file_size = ?,
			updated_at = datetime('now')
		WHERE id = ?
	`, newHash, newStorePath, newMime, newSize, itemID); err != nil {
		return fmt.Errorf("update item primary: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM item_files WHERE id = ?`, fileID); err != nil {
		return fmt.Errorf("delete promoted row: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO item_files (item_id, store_path, content_hash,
			mime_type, file_size, caption, position, created_at)
		VALUES (?, ?, ?, ?, ?, '', 0, datetime('now'))
	`, itemID, oldStorePath, oldHash, oldMime, oldSize); err != nil {
		return fmt.Errorf("insert demoted row: %w", err)
	}
	// Push every other row down by one so the demoted primary sits
	// at position 0 cleanly.
	if _, err := tx.ExecContext(ctx, `
		UPDATE item_files SET position = position + 1
		WHERE item_id = ? AND id != last_insert_rowid() AND position >= 0
	`, itemID); err != nil {
		return fmt.Errorf("shift positions: %w", err)
	}
	return tx.Commit()
}

// MergeItems folds every source item into target: source files
// become attached files of target, source tags union into target,
// source notes append below target's notes (separated by "---"),
// and the source rows are deleted. The target's existing primary
// file becomes attachment position 0 of any new files that were
// promoted in from sources, behind the merged-in source primaries.
// Source primaries land as new item_files rows; their attached
// files are re-parented to target.
func (s *SQLiteStore) MergeItems(ctx context.Context, targetID string, sourceIDs []string) (*model.Item, error) {
	if len(sourceIDs) == 0 {
		return nil, fmt.Errorf("no sources provided")
	}
	for _, sid := range sourceIDs {
		if sid == targetID {
			return nil, fmt.Errorf("source %s is the target", sid)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	// Determine the position to start appending merged files from.
	var maxPos sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(position) FROM item_files WHERE item_id = ?`, targetID).
		Scan(&maxPos); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("max position: %w", err)
	}
	nextPos := 0
	if maxPos.Valid {
		nextPos = int(maxPos.Int64) + 1
	}

	for _, srcID := range sourceIDs {
		src, err := s.scanSourceItem(ctx, tx, srcID)
		if err != nil {
			return nil, err
		}

		// 1. Source primary file → new item_files row on target.
		if src.ContentHash != "" {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO item_files (item_id, store_path, content_hash,
					mime_type, file_size, caption, position, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
				ON CONFLICT(item_id, content_hash) DO NOTHING
			`, targetID, src.StorePath, src.ContentHash, src.MimeType,
				src.FileSize, src.Title, nextPos)
			if err != nil {
				return nil, fmt.Errorf("insert source primary: %w", err)
			}
			nextPos++
		}

		// 2. Source's attached files → re-parent to target, append.
		rows, err := tx.QueryContext(ctx, `
			SELECT id, store_path, content_hash, mime_type, file_size, caption
			FROM item_files WHERE item_id = ? ORDER BY position ASC, id ASC
		`, srcID)
		if err != nil {
			return nil, fmt.Errorf("list source files: %w", err)
		}
		type srcFile struct {
			id    int64
			sp    string
			ch    string
			mt    string
			sz    int64
			cap   string
		}
		var srcFiles []srcFile
		for rows.Next() {
			var f srcFile
			if err := rows.Scan(&f.id, &f.sp, &f.ch, &f.mt, &f.sz, &f.cap); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan source file: %w", err)
			}
			srcFiles = append(srcFiles, f)
		}
		rows.Close()
		for _, f := range srcFiles {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO item_files (item_id, store_path, content_hash,
					mime_type, file_size, caption, position, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
				ON CONFLICT(item_id, content_hash) DO NOTHING
			`, targetID, f.sp, f.ch, f.mt, f.sz, f.cap, nextPos); err != nil {
				return nil, fmt.Errorf("re-parent file: %w", err)
			}
			nextPos++
		}

		// 3. Tags — union into target.
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO item_tags (item_id, tag_id)
			SELECT ?, tag_id FROM item_tags WHERE item_id = ?
		`, targetID, srcID); err != nil {
			return nil, fmt.Errorf("union tags: %w", err)
		}

		// 4. Notes — append source notes below target's notes.
		var srcNotes string
		if err := tx.QueryRowContext(ctx,
			`SELECT notes FROM items WHERE id = ?`, srcID).Scan(&srcNotes); err != nil {
			return nil, fmt.Errorf("read source notes: %w", err)
		}
		if strings.TrimSpace(srcNotes) != "" {
			var tgtNotes string
			if err := tx.QueryRowContext(ctx,
				`SELECT notes FROM items WHERE id = ?`, targetID).Scan(&tgtNotes); err != nil {
				return nil, fmt.Errorf("read target notes: %w", err)
			}
			combined := strings.TrimSpace(srcNotes)
			if strings.TrimSpace(tgtNotes) != "" {
				combined = strings.TrimRight(tgtNotes, "\n") + "\n\n---\n\n" + strings.TrimSpace(srcNotes)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE items SET notes = ?, updated_at = datetime('now') WHERE id = ?
			`, combined, targetID); err != nil {
				return nil, fmt.Errorf("update target notes: %w", err)
			}
		}

		// 4b. Extracted text — same append-with-separator treatment
		//     as notes, but kept in the dedicated extracted_text
		//     column so the FTS5 index stays coherent and the
		//     detail-view "Recognized text" / "Transcript" section
		//     surfaces the combined body. Empty target absorbs
		//     source's text wholesale; both populated → divider.
		//     This is the path that lets the user fold an OCR
		//     snippet (photo of a book passage) or a transcript
		//     (Recorder voice memo) into an existing item without
		//     losing the recognized text.
		var srcET string
		if err := tx.QueryRowContext(ctx,
			`SELECT extracted_text FROM items WHERE id = ?`, srcID).Scan(&srcET); err != nil {
			return nil, fmt.Errorf("read source extracted_text: %w", err)
		}
		if strings.TrimSpace(srcET) != "" {
			var tgtET string
			if err := tx.QueryRowContext(ctx,
				`SELECT extracted_text FROM items WHERE id = ?`, targetID).Scan(&tgtET); err != nil {
				return nil, fmt.Errorf("read target extracted_text: %w", err)
			}
			combinedET := strings.TrimSpace(srcET)
			if strings.TrimSpace(tgtET) != "" {
				combinedET = strings.TrimRight(tgtET, "\n") + "\n\n---\n\n" + strings.TrimSpace(srcET)
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE items SET extracted_text = ?, updated_at = datetime('now') WHERE id = ?
			`, combinedET, targetID); err != nil {
				return nil, fmt.Errorf("update target extracted_text: %w", err)
			}
		}

		// 5. Delete the source row (cascades to item_files / item_tags).
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM items WHERE id = ?`, srcID); err != nil {
			return nil, fmt.Errorf("delete source: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetItem(ctx, targetID)
}

// scanSourceItem is a narrow read used by MergeItems — pulls the
// fields we need from the source row without the relational
// hydration `scanItem` does (we don't need tags / collections
// here since they're handled separately).
func (s *SQLiteStore) scanSourceItem(ctx context.Context, tx *sql.Tx, id string) (*model.Item, error) {
	var item model.Item
	if err := tx.QueryRowContext(ctx, `
		SELECT id, type, title, url, notes, store_path, content_hash, mime_type, file_size
		FROM items WHERE id = ?
	`, id).Scan(&item.ID, &item.Type, &item.Title, &item.URL, &item.Notes,
		&item.StorePath, &item.ContentHash, &item.MimeType, &item.FileSize); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("source item not found: %s", id)
		}
		return nil, fmt.Errorf("read source: %w", err)
	}
	return &item, nil
}
