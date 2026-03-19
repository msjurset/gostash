CREATE TABLE IF NOT EXISTS item_links (
    item_id_from TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    item_id_to   TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    label        TEXT NOT NULL DEFAULT '',
    directed     INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (item_id_from, item_id_to)
);
CREATE INDEX IF NOT EXISTS idx_item_links_to ON item_links(item_id_to);
