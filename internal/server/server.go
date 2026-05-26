package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/exif"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"
	"github.com/msjurset/gostash/internal/thumbsync"
	"github.com/msjurset/gostash/internal/credentials"
	"github.com/msjurset/gostash/internal/gemini"
	"github.com/msjurset/gostash/internal/identify"
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
	// UsageLedgerPath is the absolute path to the daemon's Gemini
	// usage JSON file (typically ~/.stash/gemini-usage.json). When
	// set, `GET /gemini-usage` serves its contents; when empty or
	// missing, the endpoint returns an empty snapshot. Surfacing
	// the daemon's spend so Mac + Android UIs can fold it into
	// their per-device totals without each running its own ledger.
	UsageLedgerPath string
	UsageRecorder   identify.UsageRecorder
}

// Handler returns the HTTP mux ready to wrap in http.Server. The
// bearer-token middleware is applied to every endpoint EXCEPT
// /healthz, which is intentionally unauthenticated so liveness
// probes (launchd, `stash serve status`, the Mac app's Pairing tab)
// can confirm the daemon is up without consulting the token file.
func (s *Server) Handler() http.Handler {
	// Public surface — no bearer required. Liveness only; never put
	// anything secret on this mux.
	publicMux := http.NewServeMux()
	publicMux.HandleFunc("GET /healthz", s.handleHealth)

	mux := http.NewServeMux()
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
	mux.HandleFunc("GET /stats", s.handleStats)
	mux.HandleFunc("POST /reindex", s.handleReindex)
	mux.HandleFunc("POST /clean-orphans", s.handleCleanOrphans)
	// Multi-file items — attached photos beyond the primary
	// store_path. Backed by the same Store methods the CLI uses.
	mux.HandleFunc("GET /items/{id}/files", s.handleListItemFiles)
	mux.HandleFunc("POST /items/{id}/files", s.handleAttachItemFile)
	mux.HandleFunc("DELETE /items/{id}/files/{fid}", s.handleDetachItemFile)
	mux.HandleFunc("POST /items/{id}/files/reorder", s.handleReorderItemFiles)
	mux.HandleFunc("POST /items/{id}/files/{fid}/primary", s.handlePromoteItemFile)
	mux.HandleFunc("GET /items/{id}/files/{fid}/blob", s.handleItemFileBlob)
	mux.HandleFunc("POST /items/merge", s.handleMergeItems)
	mux.HandleFunc("POST /items/{id}/chat", s.handleChat)
	mux.HandleFunc("POST /ai/fix", s.handleAIFix)
	mux.HandleFunc("POST /ai/summary", s.handleAISummary)
	mux.HandleFunc("POST /ai/tags", s.handleAITags)
	mux.HandleFunc("GET /pricing", s.handlePricing)
	mux.HandleFunc("GET /gemini-usage", s.handleGeminiUsage)
	mux.HandleFunc("POST /gemini-usage", s.handleRecordGeminiUsage)
	mux.HandleFunc("POST /resolve", s.handleResolve)

	// Compose: /healthz lands on publicMux, everything else hits the
	// authenticated mux. http.ServeMux uses "longest prefix wins" so
	// the exact /healthz route on publicMux takes precedence over
	// the catch-all on the protected mux.
	root := http.NewServeMux()
	root.Handle("/healthz", publicMux)
	root.Handle("/", requireBearer(s.Token, mux))
	return root
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

	// Location resolution order, highest precedence first:
	//   1. client-sent latitude/longitude form parts (source=capture)
	//   2. JPEG EXIF GPS (source=exif)
	//
	// The mobile client's OS Location API runs at the moment the
	// user takes / shares the photo, whereas EXIF is whatever the
	// camera stamped earlier (or nothing, if the share sheet
	// stripped it). Prefer the live capture value.
	if latStr, lonStr := r.FormValue("latitude"), r.FormValue("longitude"); latStr != "" && lonStr != "" {
		if lat, errA := strconv.ParseFloat(latStr, 64); errA == nil {
			if lon, errB := strconv.ParseFloat(lonStr, 64); errB == nil {
				if lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
					item.Location = &model.Location{
						Lat: lat, Lon: lon, Source: "capture",
					}
				}
			}
		}
	}
	if item.Location == nil && item.Type == model.TypeImage {
		// Fall back to EXIF — best-effort, ErrNoGPS silently skips.
		if lat, lon, gpsErr := exif.ExtractGPS(bytes.NewReader(buf.Bytes())); gpsErr == nil {
			item.Location = &model.Location{
				Lat: lat, Lon: lon, Source: "exif",
			}
		}
	}
	// Client-sent captured_at form field is authoritative when
	// present — it covers the case where the share pipeline
	// strips EXIF before the upload reaches us. Falls through to
	// our own EXIF extraction otherwise. RFC3339 / ISO-8601 UTC.
	if raw := r.FormValue("captured_at"); raw != "" {
		if t, perr := time.Parse(time.RFC3339, raw); perr == nil && !t.IsZero() {
			utc := t.UTC()
			item.CapturedAt = &utc
		}
	}
	// Client-sent OCR / transcript text. Populated by the camera
	// path (ML Kit Text Recognition on Android, Vision on Mac) and
	// by audio shares carrying a transcript (e.g. Google Recorder's
	// combined audio+transcript intent). Server never runs OCR or
	// speech recognition itself; this is purely a wire field.
	if et := r.FormValue("extracted_text"); et != "" {
		item.ExtractedText = et
	}
	// Capture time + camera info from EXIF — same flow as the CLI
	// ingest path. Skipped when the bytes don't decode as EXIF.
	if item.Type == model.TypeImage {
		if item.CapturedAt == nil {
			if t, err := exif.ExtractCaptureTime(bytes.NewReader(buf.Bytes())); err == nil && !t.IsZero() {
				utc := t.UTC()
				item.CapturedAt = &utc
			}
		}
		if cam, err := exif.ExtractCamera(bytes.NewReader(buf.Bytes())); err == nil && cam.HasAny() {
			item.Metadata = mergeCameraMetadata(item.Metadata, cam)
		}
	}

	if err := s.Store.CreateItem(r.Context(), item); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// For image uploads, generate a small JPEG thumbnail from the
	// stored blob and persist it under thumbnail_path. Async so the
	// upload response doesn't wait on the resize. Without this the
	// thumbnail-handler fallback serves the full blob — fine for
	// correctness, brutal for cellular bandwidth on phone photos.
	if item.Type == model.TypeImage {
		go func(it model.Item) {
			ctx := context.Background()
			if _, err := thumbsync.ImportImageThumbnail(ctx, s.Store, s.Files, &it); err != nil {
				log.Printf("auto-image-thumbnail %s: %v", it.ID, err)
			}
		}(*item)
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
	Title         *string   `json:"title,omitempty"`
	Notes         *string   `json:"notes,omitempty"`
	URL           *string   `json:"url,omitempty"`
	Tags          *[]string `json:"tags,omitempty"`
	Collection    *string   `json:"collection,omitempty"`
	ExtractedText *string   `json:"extracted_text,omitempty"`
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
	if body.ExtractedText != nil {
		item.ExtractedText = *body.ExtractedText
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

	// Collection updates go through dedicated add / remove primitives
	// since they live on an association table, not on items.* columns.
	// Body shape: nil → no change; "" → clear (remove from every
	// current collection); non-empty → ensure the item is in that
	// collection (creates the collection if needed via AddCollection)
	// AND removes it from any other collections it was in.
	if body.Collection != nil {
		desired := strings.TrimSpace(*body.Collection)
		// Read current collection memberships from the post-update
		// row so we diff against authoritative state.
		afterTags, err := s.Store.GetItem(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		currentNames := make([]string, 0, len(afterTags.Collections))
		for _, c := range afterTags.Collections {
			currentNames = append(currentNames, c.Name)
		}
		// Remove any that don't match the desired value.
		for _, name := range currentNames {
			if !strings.EqualFold(name, desired) {
				if err := s.Store.RemoveFromCollection(r.Context(), id, name); err != nil {
					writeError(w, http.StatusInternalServerError,
						"remove from collection: "+err.Error())
					return
				}
			}
		}
		// Add the new one (unless caller asked to clear).
		if desired != "" {
			alreadyIn := false
			for _, name := range currentNames {
				if strings.EqualFold(name, desired) {
					alreadyIn = true
					break
				}
			}
			if !alreadyIn {
				if err := s.Store.AddToCollection(r.Context(), id, desired); err != nil {
					writeError(w, http.StatusInternalServerError,
						"add to collection: "+err.Error())
					return
				}
			}
		}
	}

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

// GET /stats — same shape as `stash stats --json`. Powers the
// Android Settings "synced items on the Mac: X" panel and any
// future client that wants a single small round-trip for "how
// big is the library on the server."
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.Store.Stats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.RebuildFTS(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "reindex: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleCleanOrphans(w http.ResponseWriter, r *http.Request) {
	// 1. Get all referenced hashes from DB
	hashes, err := s.Store.AllReferencedHashes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query referenced hashes: "+err.Error())
		return
	}
	referenced := make(map[string]bool)
	for _, h := range hashes {
		referenced[h] = true
	}

	// 2. Scan FileStore for orphans
	all, err := s.Files.ListAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list files: "+err.Error())
		return
	}

	var count int
	for _, h := range all {
		if !referenced[h] {
			if err := s.Files.Delete(h); err == nil {
				count++
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"orphans_deleted": count,
	})
}

// GET /pricing — serves the Gemini pricing catalog from
// ~/.stash/gemini-pricing.json so both Mac and Android draw from
// the same source of truth. Mac is also where the file is
// authored. When the file is missing or malformed, we fall back
// to a compiled-in defaults blob so Android isn't blocked when
// the user hasn't customized rates. Body is verbatim file
// contents (or the fallback JSON), so adding new fields in the
// future doesn't require a server-side schema change — clients
// parse what they understand and ignore the rest.
func (s *Server) handlePricing(w http.ResponseWriter, r *http.Request) {
	home, err := os.UserHomeDir()
	if err == nil {
		path := filepath.Join(home, ".stash", "gemini-pricing.json")
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
	}
	// File missing / unreadable — serve compiled-in defaults so
	// clients always get a usable response. Same model set + rates
	// as the Mac's compiled fallback in GeminiUsageStore.swift.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(defaultPricingJSON))
}

// GET /gemini-usage — serves the daemon's identify-spend ledger
// from UsageLedgerPath (typically ~/.stash/gemini-usage.json).
// When the file is missing (fresh install, daemon hasn't done any
// identify yet), returns an empty snapshot with today's date so
// clients can decode and show "0 calls today" cleanly. The Mac
// and Android UIs overlay this onto their own per-device counters
// to produce a combined view of total Gemini spend.
func (s *Server) handleGeminiUsage(w http.ResponseWriter, r *http.Request) {
	if s.UsageLedgerPath != "" {
		if data, err := os.ReadFile(s.UsageLedgerPath); err == nil && len(data) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
	}
	// Empty fallback — same schema as the file, just no entries.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"today":{"by_model":{}},"all_time":{"by_model":{}},"date":%q}`, time.Now().Format("2006-01-02"))
}

// POST /gemini-usage — records usage from external clients (Android)
// so the daemon's central ledger includes spend from all platforms.
func (s *Server) handleRecordGeminiUsage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model           string `json:"model"`
		PromptTokens    int    `json:"prompt_tokens"`
		CandidateTokens int    `json:"candidate_tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if s.UsageRecorder != nil {
		s.UsageRecorder.Record(body.Model, body.PromptTokens, body.CandidateTokens)
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /resolve — resolves an op:// reference (from 1Password) to a
// plaintext secret. Used by the Android app to get the Gemini API
// key without the phone needing access to the op CLI directly.
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reference string `json:"reference"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if !strings.HasPrefix(strings.ToLower(body.Reference), "op://") {
		writeError(w, http.StatusBadRequest, "expected an op:// reference")
		return
	}
	val, err := credentials.ResolveOp(body.Reference)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resolution failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": val})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	item, err := s.Store.GetItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	apiKey, err := credentials.Load(credentials.KeyGeminiAPIKey)
	if err != nil {
		writeError(w, http.StatusFailedDependency, "Gemini API key missing: "+err.Error())
		return
	}

	client := gemini.New()
	contextInfo := fmt.Sprintf("Title: %s\nNotes: %s", item.Title, item.Notes)
	var images []gemini.Image

	// If the item is an image, include its primary blob
	if item.Type == model.TypeImage && item.ContentHash != "" {
		if rc, err := s.Files.Open(item.ContentHash); err == nil {
			if data, err := io.ReadAll(rc); err == nil {
				images = append(images, gemini.Image{
					Data:     data,
					MimeType: item.MimeType,
				})
			}
			rc.Close()
		}
	}

	// Include any attached multi-file images
	if files, err := s.Store.ListItemFiles(r.Context(), id); err == nil {
		for _, f := range files {
			if strings.HasPrefix(f.MimeType, "image/") {
				if rc, err := s.Files.Open(f.ContentHash); err == nil {
					if data, err := io.ReadAll(rc); err == nil {
						images = append(images, gemini.Image{
							Data:     data,
							MimeType: f.MimeType,
						})
					}
					rc.Close()
				}
			}
		}
	}

	res, err := client.Query(r.Context(), apiKey, contextInfo, images, body.Question)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Gemini query failed: "+err.Error())
		return
	}

	// Record usage for accounting/analytics
	if s.UsageRecorder != nil {
		s.UsageRecorder.Record(res.Model, res.PromptTokens, res.CandidatesTokens)
	}

	// Append the answer to the notes. Keep the legacy notes-append
	// for now so Mac/older clients still see the content until
	// they learn about the dedicated field.
	now := time.Now().Format("2006-01-02 15:04")
	sep := "\n\n--- Follow-up: " + now + " ---\n"
	item.Notes += sep + body.Question + "\n\n" + res.Answer

	// Dedicated chat history for better UI rendering
	item.ChatHistory = append(item.ChatHistory,
		model.ChatMessage{
			Role:      "user",
			Content:   body.Question,
			Timestamp: time.Now().UnixMilli(),
		},
		model.ChatMessage{
			Role:      "model",
			Content:   res.Answer,
			Timestamp: time.Now().UnixMilli(),
		},
	)

	if err := s.Store.UpdateItem(r.Context(), item); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update notes: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, item)
}

const defaultPricingJSON = `{
  "default_model": "gemini-2.5-flash",
  "models": {
    "gemini-2.5-flash":      { "input_per_million": 0.30, "output_per_million": 2.50 },
    "gemini-2.5-flash-lite": { "input_per_million": 0.10, "output_per_million": 0.40 },
    "gemini-2.5-pro":        { "input_per_million": 1.25, "output_per_million": 10.00 },
    "gemini-3-flash":        { "input_per_million": 0.50, "output_per_million": 3.00 },
    "gemini-3-pro":          { "input_per_million": 2.00, "output_per_million": 12.00 }
  }
}
`

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

func (s *Server) handleAIFix(w http.ResponseWriter, r *http.Request) {
	s.handleAITextTransform(w, r, "fix")
}

func (s *Server) handleAISummary(w http.ResponseWriter, r *http.Request) {
	s.handleAITextTransform(w, r, "summary")
}

func (s *Server) handleAITags(w http.ResponseWriter, r *http.Request) {
	s.handleAITextTransform(w, r, "tags")
}

func (s *Server) handleAITextTransform(w http.ResponseWriter, r *http.Request, kind string) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	apiKey, err := credentials.Load(credentials.KeyGeminiAPIKey)
	if err != nil {
		writeError(w, http.StatusFailedDependency, "Gemini API key missing: "+err.Error())
		return
	}

	client := gemini.New()
	var res gemini.QueryResult
	var queryErr error

	switch kind {
	case "fix":
		res, queryErr = client.Fix(r.Context(), apiKey, body.Text)
	case "summary":
		res, queryErr = client.Summary(r.Context(), apiKey, body.Text)
	case "tags":
		res, queryErr = client.SuggestTags(r.Context(), apiKey, body.Text)
	default:
		writeError(w, http.StatusBadRequest, "unknown transform kind: "+kind)
		return
	}

	if queryErr != nil {
		writeError(w, http.StatusInternalServerError, "AI query failed: "+queryErr.Error())
		return
	}

	if s.UsageRecorder != nil {
		s.UsageRecorder.Record(res.Model, res.PromptTokens, res.CandidatesTokens)
	}

	writeJSON(w, http.StatusOK, map[string]string{"result": res.Answer})
}

// mergeCameraMetadata writes a Camera struct into an item's
// metadata JSON under the "camera" key, preserving any other keys
// already present. Same shape as the helper of the same name in
// internal/stash so the on-disk metadata schema stays consistent
// regardless of which ingest path created the row.
func mergeCameraMetadata(existing json.RawMessage, cam exif.Camera) json.RawMessage {
	var m map[string]any
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &m)
	}
	if m == nil {
		m = make(map[string]any)
	}
	m["camera"] = cam
	out, err := json.Marshal(m)
	if err != nil {
		return existing
	}
	return out
}
