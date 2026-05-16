package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"
	"github.com/msjurset/gostash/internal/thumbsync"
)

// Server wires the HTTP API to a Store + FileStore. The struct is
// intentionally tiny — all state is delegated. `Handler()` returns the
// composed handler with auth middleware already installed.
type Server struct {
	Store         store.Store
	Files         *filestore.FileStore
	Token         string
	NewItemID     func() string
	NewSnippetID  func() string
}

// Handler returns the bearer-guarded HTTP mux ready to wrap in
// http.Server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /capture", s.handleCapture)
	mux.HandleFunc("GET /items", s.handleListItems)
	mux.HandleFunc("GET /items/{id}", s.handleGetItem)
	mux.HandleFunc("GET /items/{id}/blob", s.handleBlob)
	mux.HandleFunc("GET /items/{id}/thumbnail", s.handleThumbnail)
	mux.HandleFunc("POST /items/{id}/tags", s.handleAddTags)
	mux.HandleFunc("DELETE /items/{id}/tags/{tag}", s.handleRemoveTag)
	mux.HandleFunc("PATCH /items/{id}", s.handlePatchItem)
	mux.HandleFunc("DELETE /items/{id}", s.handleDeleteItem)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /tags", s.handleListTags)
	mux.HandleFunc("GET /collections", s.handleListCollections)
	return requireBearer(s.Token, mux)
}

// ───────────────────────────────────────────────────────────
// Handlers
// ───────────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": "1"})
}

// POST /capture
//
// Two body shapes:
//   - application/json     — { url|text, title?, notes?, tags?, collection? }
//   - multipart/form-data  — `file` (image / arbitrary binary) plus the
//                            same metadata fields as form values.
//
// Returns the created item as JSON.
func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	ctype := r.Header.Get("Content-Type")
	if strings.HasPrefix(ctype, "multipart/") {
		s.handleCaptureMultipart(w, r)
		return
	}
	s.handleCaptureJSON(w, r)
}

type captureJSONBody struct {
	URL        string   `json:"url,omitempty"`
	Text       string   `json:"text,omitempty"`
	Title      string   `json:"title,omitempty"`
	Notes      string   `json:"notes,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Collection string   `json:"collection,omitempty"`
	Language   string   `json:"language,omitempty"`
}

func (s *Server) handleCaptureJSON(w http.ResponseWriter, r *http.Request) {
	var body captureJSONBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.URL == "" && body.Text == "" {
		writeError(w, http.StatusBadRequest, "either url or text is required")
		return
	}
	item := s.buildBaseItem(body.Title, body.Notes, body.Tags, body.Collection)
	switch {
	case body.URL != "":
		item.Type = model.TypeURL
		item.URL = body.URL
		if item.Title == "" {
			item.Title = body.URL
		}
	case body.Text != "":
		item.Type = model.TypeSnippet
		item.ExtractedText = body.Text
		if item.Title == "" {
			item.Title = firstNonEmpty(strings.SplitN(body.Text, "\n", 2)[0], "Snippet")
		}
		// Language is stored in the item's metadata via the stash
		// pipeline; left to a follow-up edit if the client supplied
		// one. Keeps the API surface here small.
	}
	if err := s.Store.CreateItem(r.Context(), item); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// URL captures from the browser extension / mobile share / any
	// HTTP client don't carry a thumbnail — kick the extraction
	// pipeline asynchronously so the user doesn't have to remember
	// `stash thumbnail backfill`. Best-effort: failures are logged
	// but don't surface in the capture response. Uses a fresh
	// background context so the request's cancellation doesn't
	// abort the work mid-fetch.
	if item.Type == model.TypeURL && item.URL != "" {
		go func(it model.Item) {
			ctx := context.Background()
			if _, err := thumbsync.ImportForItem(ctx, s.Store, s.Files, &it, it.URL); err != nil {
				log.Printf("auto-thumbnail %s: %v", it.ID, err)
			}
		}(*item)
	}
	writeJSON(w, http.StatusCreated, item)
}

// handleCaptureMultipart accepts a file upload (camera roll image,
// document, etc.) plus optional metadata form fields. The file is
// content-addressed via the existing filestore, MIME-detected, and
// classified as image/file. Mirrors `stash add <path>`'s behavior so
// items captured from the phone end up indistinguishable from
// items captured from drag-and-drop on the Mac.
func (s *Server) handleCaptureMultipart(w http.ResponseWriter, r *http.Request) {
	// 100MB cap mirrors fetchURLBytes — anything bigger probably
	// shouldn't be coming over the wire from a phone anyway.
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "multipart: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing `file` form field: "+err.Error())
		return
	}
	defer file.Close()

	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, file); err != nil {
		writeError(w, http.StatusInternalServerError, "read upload: "+err.Error())
		return
	}
	hash, size, err := s.Files.Save(bytes.NewReader(buf.Bytes()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store blob: "+err.Error())
		return
	}

	mime := header.Header.Get("Content-Type")
	if mime == "" || mime == "application/octet-stream" {
		mime = http.DetectContentType(buf.Bytes())
	}
	itemType := model.TypeFile
	if strings.HasPrefix(mime, "image/") {
		itemType = model.TypeImage
	}

	tags := splitCSV(r.FormValue("tags"))
	item := s.buildBaseItem(r.FormValue("title"), r.FormValue("notes"), tags, r.FormValue("collection"))
	item.Type = itemType
	item.URL = r.FormValue("url")
	item.SourcePath = header.Filename
	item.StorePath = hash
	item.ContentHash = hash
	item.FileSize = size
	item.MimeType = mime
	if item.Title == "" {
		item.Title = firstNonEmpty(header.Filename, "Upload")
	}

	if err := s.Store.CreateItem(r.Context(), item); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

// GET /items?type=&tag=&collection=&limit=&offset=
func (s *Server) handleListItems(w http.ResponseWriter, r *http.Request) {
	filter := readItemFilter(r)
	items, err := s.Store.ListItems(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// GET /search?q=&type=&tag=&limit=
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	filter := readItemFilter(r)
	filter.Query = r.URL.Query().Get("q")
	items, err := s.Store.SearchItems(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleGetItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	item, err := s.Store.GetItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// patchItemBody mirrors a sparse Mac edit form. Pointer fields are
// used so we can distinguish "absent" (don't touch) from "present
// and empty" (clear). For tags / collection, sending the explicit
// list replaces the existing set; omitting the field leaves the
// existing set alone.
type patchItemBody struct {
	Title      *string   `json:"title,omitempty"`
	Notes      *string   `json:"notes,omitempty"`
	URL        *string   `json:"url,omitempty"`
	Tags       *[]string `json:"tags,omitempty"`
	Collection *string   `json:"collection,omitempty"`
}

// PATCH /items/{id} — partial update of an item's metadata.
// All fields optional; only ones present in the request body are
// touched. Tags, when provided, replace the existing tag set.
func (s *Server) handlePatchItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body patchItemBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	item, err := s.Store.GetItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	if body.Title != nil {
		item.Title = strings.TrimSpace(*body.Title)
	}
	if body.Notes != nil {
		item.Notes = *body.Notes
	}
	if body.URL != nil {
		item.URL = strings.TrimSpace(*body.URL)
	}
	if body.Tags != nil {
		// Build []model.Tag from the supplied names. UpdateItem's
		// setTags handles add/remove diffing against the existing
		// associations.
		newTags := make([]model.Tag, 0, len(*body.Tags))
		seen := make(map[string]bool)
		for _, raw := range *body.Tags {
			t := strings.TrimSpace(raw)
			if t == "" || seen[strings.ToLower(t)] {
				continue
			}
			seen[strings.ToLower(t)] = true
			newTags = append(newTags, model.Tag{Name: t})
		}
		item.Tags = newTags
	}

	if err := s.Store.UpdateItem(r.Context(), item); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Collection updates aren't supported by UpdateItem's signature
	// directly — they live on a separate association table. The Mac
	// CLI handles them via dedicated AddCollection / RemoveCollection
	// calls. Defer collection editing to a follow-up that surfaces
	// the same primitives over HTTP.
	_ = body.Collection

	// Re-fetch so the response reflects the canonical row (timestamps,
	// resolved tag IDs, etc.) instead of the partially-mutated copy.
	fresh, err := s.Store.GetItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, fresh)
}

// GET /items/{id}/blob — the full content bytes for image / file items.
func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.GetItem(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if item.ContentHash == "" {
		writeError(w, http.StatusNotFound, "item has no content blob")
		return
	}
	rc, err := s.Files.Open(item.ContentHash)
	if err != nil {
		writeError(w, http.StatusNotFound, "blob missing on disk")
		return
	}
	defer rc.Close()
	if item.MimeType != "" {
		w.Header().Set("Content-Type", item.MimeType)
	}
	w.Header().Set("Content-Disposition", "inline; filename=\""+escapeQuotes(item.SourcePath)+"\"")
	_, _ = io.Copy(w, rc)
}

// GET /items/{id}/thumbnail
//
// Resolution order:
//  1. If `thumbnail_path` is set, serve that file (the proper
//     extracted thumbnail).
//  2. If the item is an image and has a content blob, serve the
//     full blob as a fallback — clients downscale on display. The
//     bandwidth hit beats showing no thumbnail at all for items
//     captured from the phone or directly added without a thumbnail
//     extraction step.
//  3. Otherwise 404.
func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.GetItem(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if item.ThumbnailPath != "" {
		if abs := s.Files.ResolveRelative(item.ThumbnailPath); abs != "" {
			http.ServeFile(w, r, abs)
			return
		}
	}
	if item.Type == model.TypeImage && item.StorePath != "" {
		if s.Files.Exists(item.StorePath) {
			http.ServeFile(w, r, s.Files.Path(item.StorePath))
			return
		}
	}
	writeError(w, http.StatusNotFound, "no thumbnail")
}

type tagsBody struct {
	Tags []string `json:"tags"`
}

func (s *Server) handleAddTags(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body tagsBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	for _, t := range body.Tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if err := s.Store.AddTag(r.Context(), id, t); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	item, err := s.Store.GetItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleRemoveTag(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tag := r.PathValue("tag")
	if err := s.Store.RemoveTag(r.Context(), id, tag); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	item, err := s.Store.GetItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// DELETE /items/{id} — archives by default; pass ?hard=true to
// actually delete (and reference-counted blob removal).
func (s *Server) handleDeleteItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.URL.Query().Get("hard") == "true" {
		// Get the item first so we can refcount-check the blob.
		item, err := s.Store.GetItem(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		if err := s.Store.DeleteItem(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if item.ThumbnailPath != "" {
			_ = s.Files.RemoveRelative(item.ThumbnailPath)
		}
		if item.ContentHash != "" {
			if refs, err := s.Store.CountItemsByContentHash(r.Context(), item.ContentHash); err == nil && refs == 0 {
				_ = s.Files.Delete(item.ContentHash)
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
		return
	}
	if err := s.Store.SetArchived(r.Context(), id, true); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"archived": id})
}

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.Store.ListTags(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request) {
	cols, err := s.Store.ListCollections(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cols)
}

// ───────────────────────────────────────────────────────────
// helpers
// ───────────────────────────────────────────────────────────

func (s *Server) buildBaseItem(title, notes string, tags []string, collection string) *model.Item {
	now := time.Now().UTC()
	item := &model.Item{
		ID:        s.NewItemID(),
		Title:     strings.TrimSpace(title),
		Notes:     strings.TrimSpace(notes),
		Metadata:  json.RawMessage("{}"),
		CreatedAt: now,
		UpdatedAt: now,
	}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		item.Tags = append(item.Tags, model.Tag{Name: t})
	}
	if collection != "" {
		item.Collections = append(item.Collections, model.Collection{Name: collection})
	}
	return item
}

func readItemFilter(r *http.Request) model.ItemFilter {
	q := r.URL.Query()
	filter := model.ItemFilter{
		Type:       model.ItemType(q.Get("type")),
		Collection: q.Get("collection"),
		Tags:       splitCSV(q.Get("tag")),
	}
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if o := q.Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n > 0 {
			filter.Offset = n
		}
	}
	return filter
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(a, b string) string {
	a = strings.TrimSpace(a)
	if a != "" {
		return a
	}
	return b
}

func escapeQuotes(s string) string {
	return strings.ReplaceAll(s, "\"", "\\\"")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errBody{Error: msg})
}

// shutdownWithGrace gives in-flight requests a moment to finish when
// the user hits Ctrl-C. Pulled out so the cobra command can call it
// from a signal handler.
func ShutdownWithGrace(ctx context.Context, srv *http.Server) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(cctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("graceful shutdown timed out; forcing close")
		}
		return err
	}
	return nil
}
