// Package thumbsync orchestrates the URL-to-thumbnail pipeline that
// turns a webpage's URL into a stored item thumbnail. Lives in
// internal/ rather than cmd/stash/ so both the CLI (`stash thumbnail
// import`, `stash thumbnail backfill`) and the HTTP server
// (`stash serve`'s /capture handler, which auto-extracts on URL
// captures) can share the same code path.
package thumbsync

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "golang.org/x/image/webp"

	"github.com/msjurset/gostash/internal/extract"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"
)

// minThumbnailDim is the floor for an "actual" thumbnail. Anything
// smaller than this on either dimension is rejected as a tracking
// pixel / layout spacer. 80 was chosen empirically against the
// universe of og:image misfires we saw in production.
const minThumbnailDim = 80

// MinDim exposes the floor for external introspection. Callers
// shouldn't need to read this directly; it's here for the rare
// caller that wants to filter candidates upstream.
const MinDim = minThumbnailDim

// ImportResult describes a successful import.
type ImportResult struct {
	RelPath      string // path relative to filestore.BaseDir()
	Source       string // "direct" | "og:image" | "twitter:image" | …
	CandidateURL string // the actual image URL that was fetched
}

// ImportForItem fetches `fromURL`, extracts the best thumbnail
// candidate (or uses the body directly when it's an image
// content-type), persists it under `thumbnails/<id>.<ext>`, and
// updates the item's thumbnail_path. Errors are returned as-is —
// the CLI and HTTP server formatters decide how to surface them.
func ImportForItem(
	ctx context.Context,
	s store.Store,
	fs *filestore.FileStore,
	item *model.Item,
	fromURL string,
) (ImportResult, error) {
	body, ct, err := FetchHTTP(fromURL, "")
	if err != nil {
		return ImportResult{}, err
	}
	defer body.Close()

	// Direct image — read and persist, no scrape needed.
	if strings.HasPrefix(strings.ToLower(ct), "image/") {
		ext := extFromContentType(ct)
		relPath, err := writeThumbnail(ctx, s, fs, item, body, ext)
		if err != nil {
			return ImportResult{}, err
		}
		return ImportResult{RelPath: relPath, Source: "direct", CandidateURL: fromURL}, nil
	}

	if !strings.Contains(strings.ToLower(ct), "html") {
		return ImportResult{}, fmt.Errorf("unsupported content-type %q", ct)
	}

	// HTML — buffer, parse, walk candidates in rank order. 10MB cap
	// matches internal/fetch.URL. Try up to 5 candidates because CDN
	// flakes / 403 hot-link blocks / 404 stale og:images are common.
	htmlBytes, err := io.ReadAll(io.LimitReader(body, 10*1024*1024))
	if err != nil {
		return ImportResult{}, fmt.Errorf("read html: %w", err)
	}
	cands, err := extract.ExtractThumbnailCandidates(bytes.NewReader(htmlBytes), fromURL)
	if err != nil {
		return ImportResult{}, fmt.Errorf("extract: %w", err)
	}
	if len(cands) == 0 {
		return ImportResult{}, fmt.Errorf("no thumbnail candidates found at %s", fromURL)
	}

	const maxAttempts = 5
	lim := len(cands)
	if lim > maxAttempts {
		lim = maxAttempts
	}
	var lastErr error
	rejected := 0
	for i := 0; i < lim; i++ {
		cand := cands[i]
		imgBody, imgCT, fetchErr := FetchHTTP(cand.URL, fromURL)
		if fetchErr != nil {
			lastErr = fetchErr
			continue
		}
		if !strings.HasPrefix(strings.ToLower(imgCT), "image/") {
			imgBody.Close()
			lastErr = fmt.Errorf("not an image (%q): %s", imgCT, cand.URL)
			continue
		}
		buf, readErr := io.ReadAll(io.LimitReader(imgBody, 10*1024*1024))
		imgBody.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("read candidate body: %w", readErr)
			continue
		}
		cfg, _, decodeErr := image.DecodeConfig(bytes.NewReader(buf))
		if decodeErr != nil {
			lastErr = fmt.Errorf("decode candidate: %w", decodeErr)
			continue
		}
		if cfg.Width < minThumbnailDim || cfg.Height < minThumbnailDim {
			rejected++
			lastErr = fmt.Errorf("candidate too small (%dx%d): %s", cfg.Width, cfg.Height, cand.URL)
			continue
		}
		ext := extFromContentType(imgCT)
		relPath, writeErr := writeThumbnail(ctx, s, fs, item, bytes.NewReader(buf), ext)
		if writeErr != nil {
			lastErr = writeErr
			continue
		}
		return ImportResult{RelPath: relPath, Source: cand.Source, CandidateURL: cand.URL}, nil
	}
	if rejected > 0 && rejected == lim {
		return ImportResult{}, fmt.Errorf("no thumbnail candidates found at %s (all rejected as too small)", fromURL)
	}
	return ImportResult{}, fmt.Errorf("could not fetch any candidate (last: %v)", lastErr)
}

// DownloadImage fetches a remote URL and returns its extension
// (with leading dot, lower-case) plus a reader for the body. Caller
// owns Close. Used by `stash thumbnail set --url`.
func DownloadImage(rawURL string) (string, io.ReadCloser, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", nil, fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", nil, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return "", nil, fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}
	ext := strings.ToLower(filepath.Ext(parsed.Path))
	if ext == "" {
		ext = extFromContentType(resp.Header.Get("Content-Type"))
	}
	return ext, resp.Body, nil
}

// FetchHTTP performs a GET with a browser User-Agent and returns
// the response body + Content-Type. Caller owns Close. `referer`
// is set when non-empty — load-bearing for image-CDN candidates
// where hot-link protection 403s requests without the right
// Referer.
func FetchHTTP(rawURL, referer string) (io.ReadCloser, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, "", fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,image/*;q=0.9,*/*;q=0.8")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, "", fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// ExtFromContentType returns a "." + extension for a known image
// Content-Type, falling back to ".jpg". Exported so the CLI's
// `stash thumbnail set` (which fetches via DownloadImage but
// separately computes ext) can reuse the same mapping.
func ExtFromContentType(ct string) string {
	return extFromContentType(ct)
}

func extFromContentType(ct string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "gif"):
		return ".gif"
	default:
		return ".jpg"
	}
}

// writeThumbnail reads `src`, writes it to the filestore at
// thumbnails/<id><ext>, and updates the item's thumbnail_path
// column. Removes any pre-existing thumbnail file (which may have
// a different extension) before writing.
func writeThumbnail(
	ctx context.Context,
	s store.Store,
	fs *filestore.FileStore,
	item *model.Item,
	src io.Reader,
	ext string,
) (string, error) {
	if ext == "" {
		ext = ".jpg"
	}
	thumbsDir := filepath.Join(fs.BaseDir(), "thumbnails")
	if err := os.MkdirAll(thumbsDir, 0755); err != nil {
		return "", fmt.Errorf("create thumbnails dir: %w", err)
	}
	if item.ThumbnailPath != "" {
		fs.RemoveRelative(item.ThumbnailPath)
	}
	relPath := filepath.Join("thumbnails", item.ID+ext)
	dest := filepath.Join(fs.BaseDir(), relPath)
	out, err := os.Create(dest)
	if err != nil {
		return "", fmt.Errorf("create thumbnail file: %w", err)
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(dest)
		return "", fmt.Errorf("write thumbnail: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dest)
		return "", fmt.Errorf("close thumbnail: %w", err)
	}
	item.ThumbnailPath = relPath
	if err := s.UpdateItem(ctx, item); err != nil {
		os.Remove(dest)
		return "", fmt.Errorf("update item: %w", err)
	}
	return relPath, nil
}
