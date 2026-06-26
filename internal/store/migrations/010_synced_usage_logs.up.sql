CREATE TABLE IF NOT EXISTS synced_usage_logs (
    id TEXT PRIMARY KEY,
    model TEXT NOT NULL,
    prompt_tokens INTEGER NOT NULL,
    candidate_tokens INTEGER NOT NULL,
    created_at DATETIME NOT NULL
);
