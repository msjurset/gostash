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
	ID   int64  `json:"id"`
	Name string `json:"name"`
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

// ItemFilter holds query parameters for listing and searching items.
type ItemFilter struct {
	Query      string
	Type       ItemType
	Tags       []string
	Collection string
	LinkedTo   string
	After      *time.Time
	Before     *time.Time
	Limit      int
	Offset     int
}
