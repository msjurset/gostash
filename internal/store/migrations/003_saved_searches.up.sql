CREATE TABLE IF NOT EXISTS saved_searches (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    query       TEXT NOT NULL DEFAULT '',
    filter_json TEXT NOT NULL DEFAULT '{}'
);
