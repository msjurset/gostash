package model

import (
	"encoding/json"
	"time"
)

// ItemType represents the kind of stashed content.
type ItemType string

const (
	TypeURL     ItemType = "link" // stored as "link" in DB; displayed as "url"
	TypeSnippet ItemType = "snippet"
	TypeFile    ItemType = "file"
	TypeImage   ItemType = "image"
	TypeEmail   ItemType = "email"
)

// Display returns the user-facing name for the type.
func (t ItemType) Display() string {
	if t == TypeURL {
		return "url"
	}
	return string(t)
}

// ParseItemType converts a user-supplied type string to an ItemType.
// Accepts "url" as an alias for the stored value "link".
func ParseItemType(s string) ItemType {
	if s == "url" {
		return TypeURL
	}
	return ItemType(s)
}

// Item is the core domain entity.
type Item struct {
	ID            string          `json:"id"`
	Type          ItemType        `json:"type"`
	Title         string          `json:"title"`
	URL           string          `json:"url,omitempty"`
	Notes         string          `json:"notes,omitempty"`
	SourcePath    string          `json:"source_path,omitempty"`
	StorePath     string          `json:"store_path,omitempty"`
	ContentHash   string          `json:"content_hash,omitempty"`
	ExtractedText string          `json:"extracted_text,omitempty"`
	MimeType      string          `json:"mime_type,omitempty"`
	FileSize      int64           `json:"file_size,omitempty"`
	ThumbnailPath string          `json:"thumbnail_path,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Archived      bool            `json:"archived,omitempty"`
	Tags          []Tag           `json:"tags,omitempty"`
	Collections   []Collection    `json:"collections,omitempty"`
	Links         []Link          `json:"links,omitempty"`
}

// Tag is a label applied to items.
type Tag struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count,omitempty"`
}

// Collection is a named group of items.
type Collection struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Link represents a relationship between two items.
type Link struct {
	ItemID    string   `json:"item_id"`
	Title     string   `json:"title"`
	Type      ItemType `json:"type"`
	Label     string   `json:"label,omitempty"`
	Direction string   `json:"direction"` // "none", "outgoing", "incoming"
}

// TagEdge represents a co-occurrence between two tags.
type TagEdge struct {
	TagA   string `json:"tag_a"`
	TagB   string `json:"tag_b"`
	Weight int    `json:"weight"`
}

// TagGraph is the full tag co-occurrence graph.
type TagGraph struct {
	Nodes []Tag     `json:"nodes"`
	Edges []TagEdge `json:"edges"`
}

// StashStats holds aggregate statistics about the stash.
type StashStats struct {
	TotalItems   int            `json:"total_items"`
	TypeCounts   map[string]int `json:"type_counts"`
	TotalSize    int64          `json:"total_size_bytes"`
	TagCount     int            `json:"tag_count"`
	CollCount    int            `json:"collection_count"`
	LinkCount    int            `json:"link_count"`
	TopTags      []Tag          `json:"top_tags"`
	OldestItem   *time.Time     `json:"oldest_item,omitempty"`
	NewestItem   *time.Time     `json:"newest_item,omitempty"`
	MonthCounts  []MonthCount   `json:"month_counts,omitempty"`
}

// MonthCount holds item count for a calendar month.
type MonthCount struct {
	Month string `json:"month"`
	Count int    `json:"count"`
}

// CheckResult holds data hygiene findings.
type CheckResult struct {
	BrokenURLs     []CheckIssue `json:"broken_urls,omitempty"`
	OrphanedFiles  []string     `json:"orphaned_files,omitempty"`
	MissingFiles   []CheckIssue `json:"missing_files,omitempty"`
	DuplicateHash  []DupeGroup  `json:"duplicate_hashes,omitempty"`
}

// CheckIssue identifies an item with a problem.
type CheckIssue struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Detail string `json:"detail"`
}

// DupeGroup groups items sharing the same content hash.
type DupeGroup struct {
	Hash  string       `json:"hash"`
	Items []CheckIssue `json:"items"`
}

// SavedSearch is a named, reusable search query with filter parameters.
// `Live` flips its sidebar treatment in stash-mac: live entries render as
// Smart Collections that auto-refresh on `.stashDidIngest`; non-live
// entries are click-to-run snapshots like the original behavior.
type SavedSearch struct {
	ID     int64      `json:"id"`
	Name   string     `json:"name"`
	Query  string     `json:"query"`
	Filter ItemFilter `json:"filter"`
	Live   bool       `json:"live,omitempty"`
}

// SearchHistoryEntry is a single committed query — one the user has
// clicked a result on or pressed Enter on, not just typed. Drives
// the Recent / Frequent views in the Chrome extension and Mac
// sidebar.
type SearchHistoryEntry struct {
	Query      string    `json:"query"`
	Count      int       `json:"count"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// FeedSource is a watched feed the user has subscribed to. The poller
// reads `kind` + `url` to fetch new entries, deduping by guid against
// existing FeedCandidate rows. Default tags and collection are applied
// to candidates when the user stashes them (or auto-stashes via
// AutoStash). PollIntervalMinutes is advisory — the poller may run more
// or less often depending on the host loop.
type FeedSource struct {
	ID                  int64      `json:"id"`
	Name                string     `json:"name"`
	Kind                string     `json:"kind"`
	URL                 string     `json:"url"`
	DefaultTags         []string   `json:"default_tags,omitempty"`
	DefaultCollection   string     `json:"default_collection,omitempty"`
	AutoStash           bool       `json:"auto_stash,omitempty"`
	// FetchContent: when true, the poller fetches each new candidate's
	// article URL through the readability extractor and stores the
	// full article body as the candidate's description. Off by default
	// because it generates one HTTP request per new candidate; opt-in
	// for sources that ship thin descriptions (Hacker News, etc.).
	FetchContent        bool       `json:"fetch_content,omitempty"`
	PollIntervalMinutes int        `json:"poll_interval_minutes"`
	Enabled             bool       `json:"enabled"`
	LastPolledAt        *time.Time `json:"last_polled_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// FeedCandidate is one feed entry awaiting triage. Until the user acts,
// state is "unread"; pressing S stashes (links StashedItemID to the new
// stash row), X dismisses, Z snoozes (SnoozeUntil set). Dismissed and
// stashed rows are kept so the same guid won't re-appear if the feed
// republishes it.
type FeedCandidate struct {
	ID                  int64      `json:"id"`
	SourceID            int64      `json:"source_id"`
	SourceName          string     `json:"source_name,omitempty"` // joined-in for display
	GUID                string     `json:"guid"`
	URL                 string     `json:"url"`
	Title               string     `json:"title,omitempty"`
	Description         string     `json:"description,omitempty"`
	// DescriptionMarkdown is the Markdown-converted form of Description,
	// computed once at poll time so the Mac Inbox preview pane renders
	// instantly. Empty for legacy rows captured before the migration —
	// `stash feeds reconvert` backfills them.
	DescriptionMarkdown string     `json:"description_markdown,omitempty"`
	ThumbnailURL        string     `json:"thumbnail_url,omitempty"`
	PublishedAt         *time.Time `json:"published_at,omitempty"`
	DiscoveredAt        time.Time  `json:"discovered_at"`
	State               string     `json:"state"`
	StateChangedAt      time.Time  `json:"state_changed_at"`
	SnoozeUntil         *time.Time `json:"snooze_until,omitempty"`
	StashedItemID       string     `json:"stashed_item_id,omitempty"`
}

// FeedCandidate states.
const (
	FeedStateUnread    = "unread"
	FeedStateStashed   = "stashed"
	FeedStateDismissed = "dismissed"
	FeedStateSnoozed   = "snoozed"
)

// DupeResult groups items that are potential duplicates.
type DupeResult struct {
	Method     string       `json:"method"` // "hash", "url", "title"
	Key        string       `json:"key"`
	Similarity float64      `json:"similarity,omitempty"`
	Items      []CheckIssue `json:"items"`
}

// ItemFilter holds query parameters for listing and searching items.
type ItemFilter struct {
	Query      string     `json:"query,omitempty"`
	Type       ItemType   `json:"type,omitempty"`
	Tags       []string   `json:"tags,omitempty"`
	Collection string     `json:"collection,omitempty"`
	LinkedTo   string     `json:"linked_to,omitempty"`
	After      *time.Time `json:"after,omitempty"`
	Before     *time.Time `json:"before,omitempty"`
	Limit      int        `json:"limit,omitempty"`
	Offset     int        `json:"offset,omitempty"`
	// IncludeArchived widens the result to include archived items in
	// addition to the unarchived default. OnlyArchived narrows it to
	// just archived items. They're mutually exclusive — set at most one.
	IncludeArchived bool `json:"include_archived,omitempty"`
	OnlyArchived    bool `json:"only_archived,omitempty"`
	// ExcludeTags narrows the result to items NOT tagged with any of
	// these names. Composes with Tags (e.g. tag=ai + exclude=read)
	// for "AI articles I've read". Lower-cased server-side.
	ExcludeTags []string `json:"exclude_tags,omitempty"`
	// Untagged narrows to items with zero tag associations. Mutually
	// exclusive with Tags / ExcludeTags semantically — when true,
	// those are ignored. Useful for "captures that fell through every
	// rule".
	Untagged bool `json:"untagged,omitempty"`
	// Recent is a relative time window resolved at query time
	// (e.g. "7d", "2w", "1h"). Smart Collections store this as the
	// raw spec so each query reads the *current* "X ago"; freezing
	// it to an absolute date would defeat the purpose.
	Recent string `json:"recent,omitempty"`
	// Regex is an RE2 pattern matched against each item's title +
	// notes + URL + extracted text. Supports anchors (`^`, `$`) and
	// the usual regex syntax. A leading `!` negates the match — e.g.
	// `!^http://` means "URL doesn't start with http". Applied
	// post-SQL in Go so it composes with all other filters; not
	// indexed, so very large libraries should pair this with another
	// filter that narrows first.
	Regex string `json:"regex,omitempty"`
}

// Language returns the detected language from the item's metadata, or empty string.
func (item *Item) Language() string {
	if len(item.Metadata) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(item.Metadata, &m); err != nil {
		return ""
	}
	if lang, ok := m["language"].(string); ok {
		return lang
	}
	return ""
}
