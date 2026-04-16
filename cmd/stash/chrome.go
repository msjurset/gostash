package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/extract"
	"github.com/msjurset/gostash/internal/fetch"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
)

var chromeHostCmd = &cobra.Command{
	Use:   "chrome-host",
	Short: "Chrome Native Messaging host",
	Long: `Run as a Chrome Native Messaging host. This command is called by Chrome
automatically — you don't need to run it manually.

  stash chrome-host install    # register the native messaging manifest with Chrome`,
	RunE: runChromeHost,
}

func init() {
	rootCmd.AddCommand(chromeHostCmd)
}

type nativeRequest struct {
	Action     string   `json:"action"`
	URL        string   `json:"url,omitempty"`
	Title      string   `json:"title,omitempty"`
	Text       string   `json:"text,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Notes      string   `json:"notes,omitempty"`
	Collection string   `json:"collection,omitempty"`
	Query      string   `json:"query,omitempty"`
	Limit      int      `json:"limit,omitempty"`
}

type nativeResponse struct {
	OK          bool              `json:"ok"`
	Error       string            `json:"error,omitempty"`
	Item        *model.Item       `json:"item,omitempty"`
	Items       []model.Item      `json:"items,omitempty"`
	Exists      *bool             `json:"exists,omitempty"`
	Tags        []model.Tag       `json:"tags,omitempty"`
	Collections []model.Collection `json:"collections,omitempty"`
}

func runChromeHost(cmd *cobra.Command, args []string) error {
	if err := config.EnsureDir(); err != nil {
		return err
	}

	s, err := store.NewSQLite(config.DBPath())
	if err != nil {
		return err
	}
	defer s.Close()

	fs := filestore.New(config.FilesDir())
	os.MkdirAll(config.FilesDir(), 0755)

	ctx := context.Background()

	for {
		req, err := readNativeMessage(os.Stdin)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		resp := handleNativeRequest(ctx, s, fs, req)
		if err := writeNativeMessage(os.Stdout, resp); err != nil {
			return err
		}
	}
}

func handleNativeRequest(ctx context.Context, s store.Store, fs *filestore.FileStore, req *nativeRequest) *nativeResponse {
	switch req.Action {
	case "stash_url":
		return handleStashURL(ctx, s, fs, req)
	case "update_url":
		return handleUpdateURL(ctx, s, req)
	case "stash_text":
		return handleStashText(ctx, s, req)
	case "search":
		return handleSearch(ctx, s, req)
	case "check_url":
		return handleCheckURL(ctx, s, req)
	case "list_tags":
		return handleListTags(ctx, s)
	case "list_collections":
		return handleListCollections(ctx, s)
	default:
		return &nativeResponse{Error: fmt.Sprintf("unknown action: %s", req.Action)}
	}
}

func handleStashURL(ctx context.Context, s store.Store, fs *filestore.FileStore, req *nativeRequest) *nativeResponse {
	if req.URL == "" {
		return &nativeResponse{Error: "url is required"}
	}

	now := time.Now().UTC()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	id := ulid.MustNew(ulid.Timestamp(now), entropy).String()

	item := &model.Item{
		ID:        id,
		Type:      model.TypeURL,
		URL:       req.URL,
		Title:     req.Title,
		Notes:     req.Notes,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  json.RawMessage("{}"),
	}

	// Fetch page content
	result, err := fetch.URL(req.URL)
	if err == nil {
		if item.Title == "" {
			item.Title = result.Title
		}
		item.ExtractedText = result.ExtractedText
		item.MimeType = result.MimeType
		if len(result.Body) > 0 {
			hash, size, err := fs.Save(bytes.NewReader(result.Body))
			if err == nil {
				item.ContentHash = hash
				item.StorePath = hash
				item.FileSize = size
			}
		}
	}

	if item.Title == "" {
		item.Title = req.URL
	}

	for _, t := range req.Tags {
		item.Tags = append(item.Tags, model.Tag{Name: t})
	}
	for _, st := range extract.SuggestTags(item.MimeType) {
		item.Tags = append(item.Tags, model.Tag{Name: st})
	}
	if req.Collection != "" {
		item.Collections = append(item.Collections, model.Collection{Name: req.Collection})
	}

	if err := s.CreateItem(ctx, item); err != nil {
		return &nativeResponse{Error: fmt.Sprintf("save: %v", err)}
	}

	return &nativeResponse{OK: true, Item: item}
}

func handleStashText(ctx context.Context, s store.Store, req *nativeRequest) *nativeResponse {
	if req.Text == "" {
		return &nativeResponse{Error: "text is required"}
	}

	now := time.Now().UTC()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	id := ulid.MustNew(ulid.Timestamp(now), entropy).String()

	title := req.Title
	if title == "" {
		title = truncateStr(req.Text, 80)
	}

	item := &model.Item{
		ID:            id,
		Type:          model.TypeSnippet,
		Title:         title,
		ExtractedText: req.Text,
		MimeType:      "text/plain",
		FileSize:      int64(len(req.Text)),
		CreatedAt:     now,
		UpdatedAt:     now,
		Metadata:      json.RawMessage("{}"),
	}

	for _, t := range req.Tags {
		item.Tags = append(item.Tags, model.Tag{Name: t})
	}
	if req.Collection != "" {
		item.Collections = append(item.Collections, model.Collection{Name: req.Collection})
	}

	if err := s.CreateItem(ctx, item); err != nil {
		return &nativeResponse{Error: fmt.Sprintf("save: %v", err)}
	}

	return &nativeResponse{OK: true, Item: item}
}

func handleSearch(ctx context.Context, s store.Store, req *nativeRequest) *nativeResponse {
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}

	// Parse tag: prefixes out of the query
	query, tags := parseSearchQuery(req.Query)
	filter := model.ItemFilter{Query: query, Limit: limit, Tags: tags}

	var items []model.Item
	var err error
	if filter.Query != "" {
		items, err = s.SearchItems(ctx, filter)
	} else if len(filter.Tags) > 0 {
		items, err = s.SearchItems(ctx, filter)
	} else {
		items, err = s.ListItems(ctx, filter)
	}
	if err != nil {
		return &nativeResponse{Error: fmt.Sprintf("search: %v", err)}
	}
	if items == nil {
		items = []model.Item{}
	}

	return &nativeResponse{OK: true, Items: items}
}

func handleUpdateURL(ctx context.Context, s store.Store, req *nativeRequest) *nativeResponse {
	if req.URL == "" {
		return &nativeResponse{Error: "url is required"}
	}

	item, err := s.GetItemByURL(ctx, req.URL)
	if err != nil {
		return &nativeResponse{Error: fmt.Sprintf("find item: %v", err)}
	}

	if req.Notes != "" {
		item.Notes = req.Notes
	}

	// Merge new tags with existing
	existing := make(map[string]bool)
	for _, t := range item.Tags {
		existing[t.Name] = true
	}
	for _, t := range req.Tags {
		if !existing[t] {
			item.Tags = append(item.Tags, model.Tag{Name: t})
		}
	}

	if err := s.UpdateItem(ctx, item); err != nil {
		return &nativeResponse{Error: fmt.Sprintf("update: %v", err)}
	}

	// Handle collection change
	if req.Collection != "" {
		// Remove from old collections
		for _, c := range item.Collections {
			s.RemoveFromCollection(ctx, item.ID, c.Name)
		}
		s.AddToCollection(ctx, item.ID, req.Collection)
	}

	// Re-fetch to get updated relations
	item, _ = s.GetItemByURL(ctx, req.URL)
	return &nativeResponse{OK: true, Item: item}
}

func handleCheckURL(ctx context.Context, s store.Store, req *nativeRequest) *nativeResponse {
	if req.URL == "" {
		return &nativeResponse{Error: "url is required"}
	}

	exists, err := s.ExistsByURL(ctx, req.URL)
	if err != nil {
		return &nativeResponse{Error: fmt.Sprintf("check: %v", err)}
	}

	resp := &nativeResponse{OK: true, Exists: &exists}

	if exists {
		item, err := s.GetItemByURL(ctx, req.URL)
		if err == nil {
			resp.Item = item
		}
	}

	return resp
}

func handleListTags(ctx context.Context, s store.Store) *nativeResponse {
	tags, err := s.ListTags(ctx)
	if err != nil {
		return &nativeResponse{Error: fmt.Sprintf("list tags: %v", err)}
	}
	if tags == nil {
		tags = []model.Tag{}
	}
	return &nativeResponse{OK: true, Tags: tags}
}

func handleListCollections(ctx context.Context, s store.Store) *nativeResponse {
	cols, err := s.ListCollections(ctx)
	if err != nil {
		return &nativeResponse{Error: fmt.Sprintf("list collections: %v", err)}
	}
	if cols == nil {
		cols = []model.Collection{}
	}
	return &nativeResponse{OK: true, Collections: cols}
}

// Native messaging protocol: 4-byte little-endian length prefix + JSON

func readNativeMessage(r io.Reader) (*nativeRequest, error) {
	var length uint32
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return nil, err
	}
	if length > 1024*1024 {
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}

	var req nativeRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	return &req, nil
}

func writeNativeMessage(w io.Writer, resp *nativeResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}

	length := uint32(len(data))
	if err := binary.Write(w, binary.LittleEndian, length); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// parseSearchQuery extracts tag: prefixes from a query string.
// "tag:golang kubernetes" -> query="kubernetes", tags=["golang"]
func parseSearchQuery(input string) (string, []string) {
	var tags []string
	var rest []string
	for _, word := range strings.Fields(input) {
		if strings.HasPrefix(word, "tag:") {
			tag := strings.TrimPrefix(word, "tag:")
			if tag != "" {
				tags = append(tags, tag)
			}
		} else {
			rest = append(rest, word)
		}
	}
	return strings.Join(rest, " "), tags
}

func truncateStr(s string, max int) string {
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}
