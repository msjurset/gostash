-- search_click_log records every search-result selection — the user
-- typed a query, then clicked a specific item or pressed Enter on a
-- highlighted result. Drives the Recent / Frequent views in the
-- Chrome extension and Mac sidebar (which roll up by query) and
-- leaves per-item click stats available for future features.
--
-- Idempotent: re-runnable because the migration runner re-applies
-- every file on every startup with no version tracking.
CREATE TABLE IF NOT EXISTS search_click_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    query       TEXT    NOT NULL,
    item_id     TEXT,                          -- nullable: query committed without picking a specific row
    clicked_at  INTEGER NOT NULL
);

-- Recent / Frequent both group by query; indexing query speeds the
-- rollup. The descending clicked_at index covers the "latest N
-- clicks across all queries" path used by future per-item stats.
CREATE INDEX IF NOT EXISTS idx_search_click_log_query ON search_click_log(query);
CREATE INDEX IF NOT EXISTS idx_search_click_log_clicked_at ON search_click_log(clicked_at DESC);
CREATE INDEX IF NOT EXISTS idx_search_click_log_item_id ON search_click_log(item_id);
