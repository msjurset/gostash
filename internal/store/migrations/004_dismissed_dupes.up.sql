CREATE TABLE IF NOT EXISTS dismissed_dupes (
    item_id_a    TEXT NOT NULL,
    item_id_b    TEXT NOT NULL,
    dismissed_at DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (item_id_a, item_id_b)
);
