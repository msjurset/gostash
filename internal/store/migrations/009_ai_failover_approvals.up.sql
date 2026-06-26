CREATE TABLE IF NOT EXISTS ai_failover_approvals (
    operation TEXT PRIMARY KEY,
    approved_at DATETIME NOT NULL,
    expires_at DATETIME NOT NULL
);
