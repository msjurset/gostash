package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/rules"
)

// GET /items/{id}/files — list the attached files for an item.
// Same shape as the embedded `files` slice on GET /items/{id}
// (which is also populated). Returned as a JSON array (never null)
// so clients can iterate uniformly.
func (s *Server) handleListItemFiles(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.GetItem(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	files, err := s.Store.ListItemFiles(r.Context(), item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if files == nil {
		files = []model.ItemFile{}
	}
	writeJSON(w, http.StatusOK, files)
}

// POST /items/{id}/files — attach a file. multipart/form-data with
// `file` (required) and optional `caption` form field. Mirrors the
// shape of POST /capture for consistency. Returns the new ItemFile
// row with its allocated `id`.
func (s *Server) handleAttachItemFile(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.GetItem(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := r.ParseMultipartForm(500 << 20); err != nil {
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

	itemFile := &model.ItemFile{
		ItemID:      item.ID,
		StorePath:   hash,
		ContentHash: hash,
		MimeType:    mime,
		FileSize:    size,
		Caption:     r.FormValue("caption"),
	}
	if err := s.Store.AttachItemFile(r.Context(), itemFile); err != nil {
		// Surface the unique-constraint case as 409 so the client
		// can distinguish "duplicate, already attached" from real
		// failures.
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			writeError(w, http.StatusConflict, "file already attached to this item")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, itemFile)
}

// DELETE /items/{id}/files/{fid} — detach a file from an item.
// The blob in the content-addressed filestore is NOT deleted;
// refcount-based GC is a separate operation.
func (s *Server) handleDetachItemFile(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.GetItem(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	fid, err := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "fid must be an integer")
		return
	}
	// Confirm the file belongs to this item so a typo can't delete
	// a file from somewhere else.
	files, err := s.Store.ListItemFiles(r.Context(), item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	belongs := false
	for _, f := range files {
		if f.ID == fid {
			belongs = true
			break
		}
	}
	if !belongs {
		writeError(w, http.StatusNotFound, fmt.Sprintf("file %d not attached to item %s", fid, item.ID))
		return
	}
	if err := s.Store.DetachItemFile(r.Context(), fid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /items/{id}/files/reorder — JSON body { "order": [fid, fid, ...] }
// rewrites the position column. The order array must contain
// exactly the set of attached file IDs for this item.
func (s *Server) handleReorderItemFiles(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.GetItem(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Order []int64 `json:"order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.Store.ReorderItemFiles(r.Context(), item.ID, body.Order); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /items/{id}/files/{fid}/primary — promote an attached file
// to be the item's primary (items.store_path). The demoted primary
// gets moved into item_files at position 0 so nothing is lost.
func (s *Server) handlePromoteItemFile(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.GetItem(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	fid, err := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "fid must be an integer")
		return
	}
	files, err := s.Store.ListItemFiles(r.Context(), item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	belongs := false
	for _, f := range files {
		if f.ID == fid {
			belongs = true
			break
		}
	}
	if !belongs {
		writeError(w, http.StatusNotFound, fmt.Sprintf("file %d not attached to item %s", fid, item.ID))
		return
	}
	if err := s.Store.PromoteItemFile(r.Context(), fid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := s.Store.GetItem(r.Context(), item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// GET /items/{id}/files/{fid}/blob — serve the raw bytes of an
// attached file. Mirrors GET /items/{id}/blob for the primary.
// Validates that fid belongs to this item so a bare fid lookup
// can't leak unrelated blobs.
func (s *Server) handleItemFileBlob(w http.ResponseWriter, r *http.Request) {
	item, err := s.Store.GetItem(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	fid, err := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "fid must be an integer")
		return
	}
	files, err := s.Store.ListItemFiles(r.Context(), item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var target *model.ItemFile
	for i := range files {
		if files[i].ID == fid {
			target = &files[i]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("file %d not attached to item %s", fid, item.ID))
		return
	}
	rc, err := s.Files.Open(target.ContentHash)
	if err != nil {
		writeError(w, http.StatusNotFound, "blob missing on disk")
		return
	}
	defer rc.Close()
	if target.MimeType != "" {
		w.Header().Set("Content-Type", target.MimeType)
	}
	w.Header().Set("Content-Disposition", "inline")
	_, _ = io.Copy(w, rc)
}

// POST /items/merge — JSON body { "target": id, "sources": [id, id, ...] }
// folds the sources into target (see Store.MergeItems for the
// detailed semantics). Returns the updated target.
func (s *Server) handleMergeItems(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target  string   `json:"target"`
		Sources []string `json:"sources"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Target == "" {
		writeError(w, http.StatusBadRequest, "target required")
		return
	}
	if len(body.Sources) == 0 {
		writeError(w, http.StatusBadRequest, "sources must contain at least one id")
		return
	}
	// Resolve prefix ids → full ids up front so a typo doesn't
	// half-merge the batch.
	tgt, err := s.Store.GetItem(r.Context(), body.Target)
	if err != nil {
		writeError(w, http.StatusNotFound, "target: "+err.Error())
		return
	}
	resolved := make([]string, 0, len(body.Sources))
	for _, sid := range body.Sources {
		src, err := s.Store.GetItem(r.Context(), sid)
		if err != nil {
			writeError(w, http.StatusNotFound, "source "+sid+": "+err.Error())
			return
		}
		resolved = append(resolved, src.ID)
	}
	out, err := s.Store.MergeItems(r.Context(), tgt.ID, resolved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Capture-log entry on the surviving target so the Mac app's
	// activity view shows the merge. Mirrors what `stash merge` does
	// on the CLI side. Best-effort — a log write failure does not
	// undo the merge (the rows have already been folded).
	_ = rules.AppendEvent(rules.DefaultLogPath(config.Dir()), rules.Event{
		Timestamp: time.Now().UTC(),
		Type:      rules.EventMerge,
		ItemID:    out.ID,
		Title:     out.Title,
		Source:    "POST /items/merge",
		Sources:   resolved,
	})
	writeJSON(w, http.StatusOK, out)
}
