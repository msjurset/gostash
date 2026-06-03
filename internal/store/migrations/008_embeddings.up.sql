CREATE TABLE IF NOT EXISTS item_embeddings (
    item_id    TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    model      TEXT NOT NULL,
    vector     BLOB NOT NULL, -- Stored []float32
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
