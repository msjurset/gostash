package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/msjurset/gostash/internal/model"
)

func TestFTSPartialSearch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, title := range []string{"Test Document", "Another File", "Hello World"} {
		item := &model.Item{
			ID: title[:4], Type: model.TypeFile, Title: title,
			CreatedAt: now, UpdatedAt: now, Metadata: json.RawMessage("{}"),
		}
		if err := s.CreateItem(ctx, item); err != nil { t.Fatal(err) }
	}

	for _, q := range []string{"Test", "Tes", "te", "Document", "Hello", "test*", "hel*"} {
		items, err := s.SearchItems(ctx, model.ItemFilter{Query: q, Limit: 10})
		if err != nil {
			fmt.Printf("Query %q: ERROR: %v\n", q, err)
		} else {
			fmt.Printf("Query %q: %d results\n", q, len(items))
		}
	}
}
