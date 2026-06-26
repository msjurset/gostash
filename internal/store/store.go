package store

import (
	"context"
	"time"

	"github.com/msjurset/gostash/internal/model"
)

// FeedCandidateFilter narrows ListFeedCandidates.
// Default (zero value) returns unread candidates across all sources.
type FeedCandidateFilter struct {
	SourceID int64    // 0 = all sources
	States   []string // empty = ["unread"]
	Limit    int      // 0 = unlimited (callers should cap)
}

// ResurfaceParams controls PickResurfaceItems.
type ResurfaceParams struct {
	Limit               int           // how many items to return; default 5
	MinIdleAgo          time.Duration // skip items seen more recently than this; default 30d
	DismissCooldown     time.Duration // skip items dismissed within this window; default 6mo
}

// Store defines the persistence interface for gostash.
type Store interface {
	// Items
	CreateItem(ctx context.Context, item *model.Item) error
	GetItem(ctx context.Context, id string) (*model.Item, error)
	ListItems(ctx context.Context, filter model.ItemFilter) ([]model.Item, error)
	SearchItems(ctx context.Context, filter model.ItemFilter) ([]model.Item, error)
	UpdateItem(ctx context.Context, item *model.Item) error
	DeleteItem(ctx context.Context, id string) error
	SetArchived(ctx context.Context, id string, archived bool) error

	ExistsByURL(ctx context.Context, url string) (bool, error)
	GetItemByURL(ctx context.Context, url string) (*model.Item, error)
	GetItemByContentHash(ctx context.Context, hash string) (*model.Item, error)
	CountItemsByContentHash(ctx context.Context, hash string) (int, error)
	ListURLsWithoutContent(ctx context.Context, limit int) ([]model.Item, error)

	// Tags
	ListTags(ctx context.Context) ([]model.Tag, error)
	TagGraph(ctx context.Context) (*model.TagGraph, error)
	RenameTag(ctx context.Context, oldName, newName string) error
	AddTag(ctx context.Context, itemID, tag string) error
	RemoveTag(ctx context.Context, itemID, tag string) error

	// Links
	LinkItems(ctx context.Context, fromID, toID, label string, directed bool) error
	UnlinkItems(ctx context.Context, idA, idB string) error
	ListLinks(ctx context.Context, itemID string) ([]model.Link, error)

	// Collections
	ListCollections(ctx context.Context) ([]model.Collection, error)
	// ListCollectionsByRecentActivity returns collections sorted by
	// the newest MAX(item_collections.added_at) — backs the Mac
	// sidebar's "Recent" sort. Pass limit > 0 to cap; 0 = all.
	ListCollectionsByRecentActivity(ctx context.Context, limit int) ([]model.Collection, error)
	// ListCollectionsByFrequency returns collections sorted by
	// view_count DESC — backs the "Frequent" sort.
	ListCollectionsByFrequency(ctx context.Context, limit int) ([]model.Collection, error)
	CreateCollection(ctx context.Context, name, description string) (*model.Collection, error)
	GetCollection(ctx context.Context, name string) (*model.Collection, error)
	DeleteCollection(ctx context.Context, name string) error
	AddToCollection(ctx context.Context, itemID, collectionName string) error
	RemoveFromCollection(ctx context.Context, itemID, collectionName string) error
	ReorderCollection(ctx context.Context, name string, orderedIDs []string) error
	ListCollectionItems(ctx context.Context, name string, filter model.ItemFilter) ([]model.Item, error)
	// TouchCollection increments view_count for the Frequent sort.
	// Called by the Mac when the user navigates to a collection.
	TouchCollection(ctx context.Context, name string) error
	// MergeCollections folds every membership from `others` into
	// `survivor` (INSERT OR IGNORE so duplicates collapse), then
	// deletes the others. Single transaction so a partial failure
	// rolls back rather than half-merging. survivor must already
	// exist; missing names in `others` return an error.
	MergeCollections(ctx context.Context, survivor string, others []string) error

	// Duplicate Dismissal
	DismissDupePair(ctx context.Context, idA, idB string) error
	IsDupeDismissed(ctx context.Context, idA, idB string) bool
	ListDismissedPairs(ctx context.Context) ([][2]string, error)

	// Moments dismissal — suppress specific cluster suggestions so
	// they don't keep re-appearing in `stash moments`. Signature is
	// caller-computed (SHA-256 of sorted item IDs).
	DismissMoment(ctx context.Context, signature string, itemCount int, sampleTitle string) error
	UndismissMoment(ctx context.Context, signature string) error
	IsMomentDismissed(ctx context.Context, signature string) (bool, error)
	DismissedMomentSignatures(ctx context.Context) (map[string]bool, error)
	ListDismissedMoments(ctx context.Context) ([]model.DismissedMoment, error)

	RebuildFTS(ctx context.Context) error
	AllReferencedHashes(ctx context.Context) ([]string, error)
	DeleteOrphanedFiles(ctx context.Context) (int, error)

	// Saved Searches
	SaveSearch(ctx context.Context, name, query string, filter model.ItemFilter, live bool) error
	ListSavedSearches(ctx context.Context) ([]model.SavedSearch, error)
	GetSavedSearch(ctx context.Context, name string) (*model.SavedSearch, error)
	DeleteSavedSearch(ctx context.Context, name string) error
	RenameSavedSearch(ctx context.Context, oldName, newName string) error

	// Stats
	Stats(ctx context.Context) (*model.StashStats, error)

	// Search-click log (Recent / Frequent views).
	RecordSearchClick(ctx context.Context, query, itemID string) error
	ListSearchHistory(ctx context.Context, sortBy SearchHistorySort, limit int) ([]model.SearchHistoryEntry, error)
	ClearSearchHistory(ctx context.Context) error
	DeleteSearchHistoryEntry(ctx context.Context, query string) error

	// Feed sources (subscriptions).
	CreateFeedSource(ctx context.Context, src *model.FeedSource) error
	GetFeedSource(ctx context.Context, idOrName string) (*model.FeedSource, error)
	ListFeedSources(ctx context.Context, enabledOnly bool) ([]model.FeedSource, error)
	UpdateFeedSource(ctx context.Context, src *model.FeedSource) error
	DeleteFeedSource(ctx context.Context, idOrName string) error
	TouchFeedSourcePoll(ctx context.Context, id int64, errMsg string) error

	// Feed candidates (triage inbox).
	UpsertFeedCandidate(ctx context.Context, c *model.FeedCandidate) (created bool, err error)
	GetFeedCandidate(ctx context.Context, id int64) (*model.FeedCandidate, error)
	ListFeedCandidates(ctx context.Context, filter FeedCandidateFilter) ([]model.FeedCandidate, error)
	UpdateFeedCandidateState(ctx context.Context, id int64, state string, snoozeUntil *time.Time, stashedItemID string) error
	UpdateFeedCandidateMarkdown(ctx context.Context, id int64, markdown string) error
	UpdateFeedCandidateContent(ctx context.Context, id int64, description, markdown string) error
	ExpireSnoozedCandidates(ctx context.Context, now time.Time) (int, error)

	// Related — score every other item by how much it overlaps with
	// the given source item (shared tags, same domain, same content
	// hash, existing manual link, shared collection). Used by the
	// "Related items" section in the Mac detail pane.
	RelatedItems(ctx context.Context, source *model.Item, limit int) ([]model.Item, error)

	// Resurface — picking forgotten stash items for the Inbox's
	// "From your stash" section.
	PickResurfaceItems(ctx context.Context, params ResurfaceParams) ([]model.Item, error)
	MarkResurfaced(ctx context.Context, itemID string, now time.Time) error
	DismissResurface(ctx context.Context, itemID string, now time.Time) error
	SnoozeResurface(ctx context.Context, itemID string, until time.Time) error

	// Item files — additional attached photos beyond items.store_path.
	// The primary file remains on items.store_path so existing reads
	// keep working; rows here accumulate when the user attaches
	// further angles / states of the same subject (mushroom top /
	// side / bottom, bird male / female, etc).
	AttachItemFile(ctx context.Context, file *model.ItemFile) error
	DetachItemFile(ctx context.Context, fileID int64) error
	UpdateItemFileCaption(ctx context.Context, fileID int64, caption string) error
	ListItemFiles(ctx context.Context, itemID string) ([]model.ItemFile, error)
	ReorderItemFiles(ctx context.Context, itemID string, orderedIDs []int64) error
	PromoteItemFile(ctx context.Context, fileID int64) error
	// Merge sources' files + tags + notes into target. Sources are
	// deleted on success. Returns the updated target item.
	MergeItems(ctx context.Context, targetID string, sourceIDs []string) (*model.Item, error)

	// Embeddings & Semantic Search
	ListItemsMissingEmbeddings(ctx context.Context, limit int) ([]model.Item, error)
	SaveItemEmbedding(ctx context.Context, itemID string, model string, vector []float32) error
	GetItemEmbedding(ctx context.Context, itemID string) (modelName string, vector []float32, err error)
	SearchSemantic(ctx context.Context, queryVector []float32, filter model.ItemFilter) ([]model.Item, error)
	SearchHybrid(ctx context.Context, filter model.ItemFilter) ([]model.Item, error)

	Checkpoint() error
	Close() error

	// AI Failover
	ApproveFailover(ctx context.Context, operation string, expiresAt time.Time) error
	IsFailoverApproved(ctx context.Context, operation string) (bool, error)

	// Sync usage logs
	RegisterUsageLogs(ctx context.Context, logs []model.UsageLog) ([]model.UsageLog, error)
}
