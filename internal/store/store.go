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

	// Tags
	ListTags(ctx context.Context) ([]model.Tag, error)
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

	Close() error
}
