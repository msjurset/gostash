-- Watched-source feed subscriptions and the candidate items pulled
-- from them. Drives the "From your sources" half of the Inbox scene:
-- the user subscribes to an RSS / YouTube / etc. feed; the poller
-- writes new items into feed_candidates; the triage UI presents
-- them; the user stashes, dismisses, or snoozes each one.
--
-- Idempotent — the migration runner re-applies every file on each
-- startup with no version tracking.
-- `kind` is a free-form string so adding future source types doesn't
-- require schema changes. Phase 1 implements 'rss' only — YouTube,
-- Substack, GitHub releases, arxiv, reddit-json, Mastodon, Bluesky,
-- HN keyword filters, Google Alerts all expose RSS, so 'rss' covers
-- them at the parser layer (user pastes the feed URL). Phase 3 may
-- add 'youtube' that auto-derives the feeds URL from a channel URL.
-- Email subscriptions intentionally don't have a kind here — the
-- design routes them through the existing email ingest path with a
-- per-sender tagging rule (decision (a) in the design conversation).
CREATE TABLE IF NOT EXISTS feed_sources (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    name                  TEXT    NOT NULL,                   -- "Pretty Birds Blog"
    kind                  TEXT    NOT NULL,                   -- 'rss' for v1; reserved for 'youtube' etc.
    url                   TEXT    NOT NULL,                   -- feed URL
    default_tags          TEXT    NOT NULL DEFAULT '[]',      -- JSON array of tag names
    default_collection    TEXT    NOT NULL DEFAULT '',        -- collection name, '' = none
    auto_stash            INTEGER NOT NULL DEFAULT 0,         -- 1 = skip triage, stash directly with defaults
    poll_interval_minutes INTEGER NOT NULL DEFAULT 360,       -- 6h default
    enabled               INTEGER NOT NULL DEFAULT 1,
    last_polled_at        INTEGER,                            -- unix seconds
    last_error            TEXT    NOT NULL DEFAULT '',
    created_at            INTEGER NOT NULL,
    updated_at            INTEGER NOT NULL
);

-- Lookups by enabled+next-due drive the poller; unique name keeps
-- the UI's pick-by-name semantics safe.
CREATE UNIQUE INDEX IF NOT EXISTS idx_feed_sources_name ON feed_sources(name);
CREATE INDEX IF NOT EXISTS idx_feed_sources_enabled ON feed_sources(enabled, last_polled_at);

CREATE TABLE IF NOT EXISTS feed_candidates (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id         INTEGER NOT NULL REFERENCES feed_sources(id) ON DELETE CASCADE,
    guid              TEXT    NOT NULL,                       -- RSS guid / YouTube video id / etc.
    url               TEXT    NOT NULL,
    title             TEXT    NOT NULL DEFAULT '',
    description       TEXT    NOT NULL DEFAULT '',
    thumbnail_url     TEXT    NOT NULL DEFAULT '',
    published_at      INTEGER,                                -- nullable (some feeds omit dates)
    discovered_at     INTEGER NOT NULL,
    state             TEXT    NOT NULL DEFAULT 'unread',      -- 'unread' | 'stashed' | 'dismissed' | 'snoozed'
    state_changed_at  INTEGER NOT NULL,
    snooze_until      INTEGER,                                -- unix seconds, valid when state='snoozed'
    stashed_item_id   TEXT,                                   -- set when state='stashed'; nulled if the item is later deleted
    UNIQUE(source_id, guid)
);

-- "Unread per source" is the hot query for triage rendering. The
-- snooze-expiry sweep uses the (state, snooze_until) pair.
CREATE INDEX IF NOT EXISTS idx_feed_cand_source_state ON feed_candidates(source_id, state);
CREATE INDEX IF NOT EXISTS idx_feed_cand_state_snooze ON feed_candidates(state, snooze_until);
CREATE INDEX IF NOT EXISTS idx_feed_cand_discovered ON feed_candidates(discovered_at DESC);

-- Sidecar table for Inbox Resurface state. Kept separate from
-- `items` so adding/removing the feature doesn't widen the core
-- items schema (every SELECT * on items would otherwise need
-- updating). PK = item_id keeps the relationship 1:1.
CREATE TABLE IF NOT EXISTS item_resurface_state (
    item_id                 TEXT    PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    last_resurfaced_at      INTEGER,
    resurface_dismissed_at  INTEGER,
    resurface_snoozed_until INTEGER
);
CREATE INDEX IF NOT EXISTS idx_item_resurface_dismissed ON item_resurface_state(resurface_dismissed_at);
CREATE INDEX IF NOT EXISTS idx_item_resurface_snoozed   ON item_resurface_state(resurface_snoozed_until);
