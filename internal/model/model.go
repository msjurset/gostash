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
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
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
type SavedSearch struct {
	ID     int64      `json:"id"`
	Name   string     `json:"name"`
	Query  string     `json:"query"`
	Filter ItemFilter `json:"filter"`
}

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
