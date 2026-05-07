package store

import (
	"context"

	"github.com/msjurset/gostash/internal/model"
)

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
	CreateCollection(ctx context.Context, name, description string) (*model.Collection, error)
	GetCollection(ctx context.Context, name string) (*model.Collection, error)
	DeleteCollection(ctx context.Context, name string) error
	AddToCollection(ctx context.Context, itemID, collectionName string) error
	RemoveFromCollection(ctx context.Context, itemID, collectionName string) error
	ListCollectionItems(ctx context.Context, name string, filter model.ItemFilter) ([]model.Item, error)

	// Duplicate Dismissal
	DismissDupePair(ctx context.Context, idA, idB string) error
	IsDupeDismissed(ctx context.Context, idA, idB string) bool
	ListDismissedPairs(ctx context.Context) ([][2]string, error)

	// Saved Searches
	SaveSearch(ctx context.Context, name, query string, filter model.ItemFilter, live bool) error
	ListSavedSearches(ctx context.Context) ([]model.SavedSearch, error)
	GetSavedSearch(ctx context.Context, name string) (*model.SavedSearch, error)
	DeleteSavedSearch(ctx context.Context, name string) error
	RenameSavedSearch(ctx context.Context, oldName, newName string) error

	// Stats
	Stats(ctx context.Context) (*model.StashStats, error)

	Checkpoint() error
	Close() error
}
