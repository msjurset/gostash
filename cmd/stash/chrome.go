package main

import (
	"bytes"
	"context"
	"encoding/base64"
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

	// fetch_url_pick: which URLs to download from a previously-listed
	// page. Either Picks (HTML mode) or URL alone (direct-file mode).
	Picks []string `json:"picks,omitempty"`
	// fetch_url_pick: link-source can be a URL or an item id; the
	// new items are linked to that source. Auto-creates a URL item
	// when a URL is given that isn't yet stashed.
	LinkSource string `json:"link_source,omitempty"`
	// fetch_url_pick: when true, bundle picks into a single
	// zip-typed file item instead of N individual items.
	Archive bool `json:"archive,omitempty"`
	// fetch_url_pick: when true, also cross-link every pair of
	// imported picks (clique rim edges) in addition to any
	// source-spoke from LinkSource.
	Clique bool `json:"clique,omitempty"`
	// fetch_url_list: include every <a href> regardless of file
	// extension (default: only allowlisted extensions).
	AllLinks bool `json:"all_links,omitempty"`

	// append_notes: target item for appending a selection. Source
	// URL/title/date populate the attribution header so the user
	// can trace where the snippet came from inside the note.
	ItemID      string `json:"item_id,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
	SourceTitle string `json:"source_title,omitempty"`

	// stash_blob: pre-fetched bytes from the Chrome extension.
	// Used for auth-gated CDN URLs (Gemini chat attachments,
	// GDrive previews, etc.) where the native host's anonymous
	// HTTP fetch would 403 — the extension fetches with its host
	// permissions + cookie context and ships the bytes here.
	BlobBase64 string `json:"blob_base64,omitempty"`
	BlobMIME   string `json:"blob_mime,omitempty"`

	// search_history_list: "recent" or "frequent" rollup of the
	// click log. search_history_record: ItemID is the optional
	// clicked-item ID — Query carries the search criteria.
	Sort string `json:"sort,omitempty"`
}

type nativeResponse struct {
	OK          bool              `json:"ok"`
	Error       string            `json:"error,omitempty"`
	Item        *model.Item       `json:"item,omitempty"`
	Items       []model.Item      `json:"items,omitempty"`
	Exists      *bool             `json:"exists,omitempty"`
	Tags        []model.Tag       `json:"tags,omitempty"`
	Collections []model.Collection `json:"collections,omitempty"`

	// fetch_url_list response.
	PageType   string          `json:"page_type,omitempty"` // "page" | "direct"
	PageURL    string          `json:"page_url,omitempty"`
	PageTitle  string          `json:"page_title,omitempty"`
	Candidates []pageCandidate `json:"candidates,omitempty"`
	DirectMIME string          `json:"direct_mime,omitempty"`
	DirectSize int64           `json:"direct_size,omitempty"`

	// fetch_url_pick response.
	Imported []pickedItem `json:"imported,omitempty"`
	LinkedTo string       `json:"linked_to,omitempty"`

	// search_history_list response.
	History []model.SearchHistoryEntry `json:"history,omitempty"`
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
	case "fetch_url_list":
		return handleFetchURLList(req)
	case "fetch_url_pick":
		return handleFetchURLPick(s, fs, req)
	case "append_notes":
		return handleAppendNotes(ctx, s, req)
	case "stash_blob":
		return handleStashBlob(s, fs, req)
	case "search_history_record":
		return handleSearchHistoryRecord(ctx, s, req)
	case "search_history_list":
		return handleSearchHistoryList(ctx, s, req)
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

	// Apply URL-exclusion rules from config.toml before storing,
	// so captures from session-only pages (Gemini, OAuth, etc.)
	// don't pollute the item's URL column.
	storedURL, _ := config.RedactURL(req.URL)
	item := &model.Item{
		ID:        id,
		Type:      model.TypeURL,
		URL:       storedURL,
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

	// Apply user-defined rules. Same helper as the CLI add path so Chrome
	// captures get the same tags, retitles, notes, notifications, links,
	// and skip behavior as `stash add`.
	ruleResult := ApplyRulesToItem(s, item, RuleApplyContext{
		UserTitle:      req.Title,
		UserNote:       req.Notes,
		UserCollection: req.Collection,
	})
	if ruleResult.Skipped {
		logSkipped(item, ruleResult)
		for _, msg := range ruleResult.Notifies {
			fireNotification(item, msg)
		}
		return &nativeResponse{Error: fmt.Sprintf("skipped by rule %q", ruleResult.SkippedBy)}
	}
	EnsureRuleCollections(ctx, s, ruleResult)

	if err := s.CreateItem(ctx, item); err != nil {
		LogCaptureError(sourceFor(item), err.Error())
		return &nativeResponse{Error: fmt.Sprintf("save: %v", err)}
	}

	logRuleFire(item, ruleResult)
	logCapture(item, ruleResult)
	FirePostSaveRuleEffects(ctx, s, item, ruleResult)

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
		// Optional: persist the source page URL so the user can
		// click back to where the selection came from. Snippets
		// don't usually carry URLs, but for browser-captured
		// selections it's the most useful provenance signal.
		// Redacted via config.RedactURL so session-only sources
		// don't pollute the URL column.
		URL: func() string { u, _ := config.RedactURL(req.URL); return u }(),
	}

	for _, t := range req.Tags {
		item.Tags = append(item.Tags, model.Tag{Name: t})
	}
	if req.Collection != "" {
		item.Collections = append(item.Collections, model.Collection{Name: req.Collection})
	}

	// Same rules application as the URL path — text snippets captured via
	// the Chrome extension get the same tag/note/skip treatment as `stash add -`.
	ruleResult := ApplyRulesToItem(s, item, RuleApplyContext{
		UserTitle:      req.Title,
		UserNote:       req.Notes,
		UserCollection: req.Collection,
	})
	if ruleResult.Skipped {
		logSkipped(item, ruleResult)
		for _, msg := range ruleResult.Notifies {
			fireNotification(item, msg)
		}
		return &nativeResponse{Error: fmt.Sprintf("skipped by rule %q", ruleResult.SkippedBy)}
	}
	EnsureRuleCollections(ctx, s, ruleResult)

	if err := s.CreateItem(ctx, item); err != nil {
		LogCaptureError(sourceFor(item), err.Error())
		return &nativeResponse{Error: fmt.Sprintf("save: %v", err)}
	}

	logRuleFire(item, ruleResult)
	logCapture(item, ruleResult)
	FirePostSaveRuleEffects(ctx, s, item, ruleResult)

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

// handleFetchURLList wraps `stash fetch-url --list`. Returns either
// a "page" response with candidate URLs, or a "direct" response
// with metadata about a single downloadable file. The Chrome
// extension uses this to populate its file picker.
func handleFetchURLList(req *nativeRequest) *nativeResponse {
	if req.URL == "" {
		return &nativeResponse{Error: "url is required"}
	}
	body, ctype, finalURL, err := fetchURLBytes(req.URL, "")
	if err != nil {
		return &nativeResponse{Error: fmt.Sprintf("fetch: %v", err)}
	}
	mainType := strings.ToLower(strings.TrimSpace(strings.SplitN(ctype, ";", 2)[0]))
	if mainType == "text/html" {
		page, err := scrapeCandidates(finalURL, body, req.AllLinks)
		if err != nil {
			return &nativeResponse{Error: fmt.Sprintf("scrape: %v", err)}
		}
		return &nativeResponse{
			OK:         true,
			PageType:   "page",
			PageURL:    finalURL,
			PageTitle:  page.PageTitle,
			Candidates: page.Candidates,
		}
	}
	return &nativeResponse{
		OK:         true,
		PageType:   "direct",
		PageURL:    finalURL,
		DirectMIME: ctype,
		DirectSize: int64(len(body)),
	}
}

// handleAppendNotes appends `Text` to the target item's notes
// field with a small attribution header so the user can trace
// the snippet back to its source page. Uses the existing
// UpdateItem store method — no new schema needed.
//
// The attribution header looks like:
//
//	--- From <source_title> (<source_url>) on <YYYY-MM-DD> ---
//
// fields are skipped when empty so a manual append with no source
// context still works (just prepends a thin separator).
func handleAppendNotes(ctx context.Context, s store.Store, req *nativeRequest) *nativeResponse {
	if req.ItemID == "" {
		return &nativeResponse{Error: "item_id is required"}
	}
	if strings.TrimSpace(req.Text) == "" {
		return &nativeResponse{Error: "text is required"}
	}
	item, err := s.GetItem(ctx, req.ItemID)
	if err != nil {
		return &nativeResponse{Error: fmt.Sprintf("find item: %v", err)}
	}

	header := buildAttributionHeader(req.SourceTitle, req.SourceURL)
	addition := req.Text
	if header != "" {
		addition = header + "\n" + addition
	}

	if strings.TrimSpace(item.Notes) == "" {
		item.Notes = addition
	} else {
		item.Notes = strings.TrimRight(item.Notes, "\n") + "\n\n" + addition
	}
	item.UpdatedAt = time.Now().UTC()

	if err := s.UpdateItem(ctx, item); err != nil {
		return &nativeResponse{Error: fmt.Sprintf("update: %v", err)}
	}
	return &nativeResponse{OK: true, Item: item}
}

// handleStashBlob accepts pre-fetched bytes from the Chrome extension
// (base64) and stashes them as an item. Used for auth-gated CDN URLs
// where the native host's anonymous HTTP fetch can't authenticate —
// the extension does the fetch with its host permissions + page
// cookies and ships the bytes here. Reuses `stashFetchedBytes` so
// MIME-detection / type-classification / filestore wiring is the
// same path as `fetch_url_pick`.
func handleStashBlob(s store.Store, fs *filestore.FileStore, req *nativeRequest) *nativeResponse {
	if req.URL == "" {
		return &nativeResponse{Error: "url is required"}
	}
	if req.BlobBase64 == "" {
		return &nativeResponse{Error: "blob_base64 is required"}
	}
	body, err := base64.StdEncoding.DecodeString(req.BlobBase64)
	if err != nil {
		return &nativeResponse{Error: fmt.Sprintf("decode base64: %v", err)}
	}

	item, err := stashFetchedBytes(s, fs, req.URL, req.BlobMIME, body, req.Tags, req.Collection, req.Title)
	if err != nil {
		return &nativeResponse{Error: fmt.Sprintf("stash: %v", err)}
	}

	resp := &nativeResponse{OK: true, Item: item}

	// Optional link-source: works the same as fetch_url_pick. If
	// the extension passes a page URL it'll be auto-created as a
	// URL item when not already in the stash.
	if req.LinkSource != "" {
		linkID, err := resolveLinkSource(s, req.LinkSource)
		if err != nil {
			resp.Error = fmt.Sprintf("link-source: %v", err)
			return resp
		}
		if err := s.LinkItems(context.Background(), item.ID, linkID, "from-page", false); err != nil {
			resp.Error = fmt.Sprintf("link: %v", err)
			return resp
		}
		resp.LinkedTo = linkID
	}
	return resp
}

func buildAttributionHeader(title, srcURL string) string {
	if title == "" && srcURL == "" {
		return ""
	}
	var parts []string
	if title != "" {
		parts = append(parts, title)
	}
	if srcURL != "" {
		parts = append(parts, "("+srcURL+")")
	}
	parts = append(parts, "on "+time.Now().UTC().Format("2006-01-02"))
	return "--- From " + strings.Join(parts, " ") + " ---"
}

// handleFetchURLPick wraps `stash fetch-url --pick`. Downloads each
// URL in `Picks`, stashes them, links to the source page item if
// `LinkSource` is given, optionally bundles into a zip when
// `Archive` is true. Returns the list of created items.
func handleFetchURLPick(s store.Store, fs *filestore.FileStore, req *nativeRequest) *nativeResponse {
	if len(req.Picks) == 0 {
		return &nativeResponse{Error: "picks is required"}
	}

	pageURL := req.URL // used as Referer for hot-link CDNs
	imported := make([]pickedItem, 0, len(req.Picks))
	var errors []string

	var linkTargetID string
	if req.LinkSource != "" {
		id, err := resolveLinkSource(s, req.LinkSource)
		if err != nil {
			return &nativeResponse{Error: fmt.Sprintf("link-source: %v", err)}
		}
		linkTargetID = id
	}

	tags := req.Tags
	collection := req.Collection

	if req.Archive {
		archiveItem, _, err := stashAsArchive(s, fs, pageURL, req.Picks, tags, collection)
		if err != nil {
			return &nativeResponse{Error: fmt.Sprintf("archive: %v", err)}
		}
		imported = append(imported, pickedItem{
			ID:    archiveItem.ID,
			URL:   archiveItem.URL,
			Title: archiveItem.Title,
			Type:  string(archiveItem.Type),
		})
	} else {
		ctx := context.Background()
		for _, pick := range req.Picks {
			body, ctype, finalURL, err := fetchURLBytes(pick, pageURL)
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", pick, err))
				continue
			}
			item, err := stashFetchedBytes(s, fs, finalURL, ctype, body, tags, collection, "")
			if err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", pick, err))
				continue
			}
			imported = append(imported, pickedItem{
				ID: item.ID, URL: finalURL, Title: item.Title, Type: string(item.Type),
			})
			if linkTargetID != "" {
				if err := s.LinkItems(ctx, item.ID, linkTargetID, "from-page", false); err != nil {
					errors = append(errors, fmt.Sprintf("link %s: %v", item.ID, err))
				}
			}
		}
		// Clique rim — N×(N−1)/2 mutual edges across imported. The
		// Chrome surface trusts the user's explicit toggle and does
		// not warn past the CLI's soft 15-pick threshold.
		if req.Clique && len(imported) >= 2 {
			ctx := context.Background()
			for i := 0; i < len(imported); i++ {
				for j := i + 1; j < len(imported); j++ {
					if err := s.LinkItems(ctx, imported[i].ID, imported[j].ID, "clique", false); err != nil {
						errors = append(errors, fmt.Sprintf("clique link: %v", err))
					}
				}
			}
		}
	}

	resp := &nativeResponse{
		OK:       true,
		Imported: imported,
		LinkedTo: linkTargetID,
	}
	if len(errors) > 0 {
		// Surface a partial-success error string. The extension
		// can still show what got imported AND warn about misses.
		resp.Error = strings.Join(errors, "; ")
	}
	return resp
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

// handleSearchHistoryRecord appends one row to search_click_log.
// req.Query is the search criteria committed-to; req.ItemID is
// the (optional) clicked item id.
func handleSearchHistoryRecord(ctx context.Context, s store.Store, req *nativeRequest) *nativeResponse {
	if err := s.RecordSearchClick(ctx, req.Query, req.ItemID); err != nil {
		return &nativeResponse{Error: fmt.Sprintf("record: %v", err)}
	}
	return &nativeResponse{OK: true}
}

// handleSearchHistoryList rolls up the click log into Recent or
// Frequent ordering and returns the top req.Limit (default 20).
func handleSearchHistoryList(ctx context.Context, s store.Store, req *nativeRequest) *nativeResponse {
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	sort := store.SearchHistoryRecent
	if req.Sort == "frequent" {
		sort = store.SearchHistoryFrequent
	}
	entries, err := s.ListSearchHistory(ctx, sort, limit)
	if err != nil {
		return &nativeResponse{Error: fmt.Sprintf("list: %v", err)}
	}
	return &nativeResponse{OK: true, History: entries}
}
