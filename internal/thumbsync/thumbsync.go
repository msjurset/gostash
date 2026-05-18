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
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/msjurset/gostash/internal/exif"
	"github.com/msjurset/gostash/internal/extract"
	"github.com/msjurset/gostash/internal/filestore"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/store"
)

// MaxThumbnailDim is the target long-edge size for generated
// thumbnails from image-blob uploads. Picked so the resulting JPEG
// is small enough for cellular thumbnail loads (10-50 KB typical)
// while still readable at the Browse-row sizes the Mac and Android
// apps render.
const MaxThumbnailDim = 512

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

// GenerateImageThumbnail decodes the image bytes from `src`, scales
// the result to fit within MaxThumbnailDim on the long edge while
// preserving aspect ratio, and re-encodes as JPEG (quality 85).
// Used at multipart-capture time so mobile-captured photos don't
// ship full-resolution blobs as their thumbnail.
//
// Skips the scale-up case: images already smaller than the target
// pass through their dimensions unchanged (still re-encoded as JPEG
// for consistency).
//
// Returns the encoded bytes and the file extension to use (".jpg").
func GenerateImageThumbnail(src io.Reader) ([]byte, string, error) {
	// Read once into memory so we can hand the same bytes to both
	// image.Decode and exif.Orientation. Source images are O(1-10
	// MB) so a single buffered copy is fine.
	all, err := io.ReadAll(src)
	if err != nil {
		return nil, "", fmt.Errorf("read source: %w", err)
	}
	img, _, err := image.Decode(bytes.NewReader(all))
	if err != nil {
		return nil, "", fmt.Errorf("decode: %w", err)
	}
	// EXIF Orientation tag must be applied before resize, otherwise
	// the saved thumbnail bakes in sensor-orientation pixels and
	// every downstream consumer renders it sideways. Go's
	// image.Decode does not honor the tag.
	img = applyOrientation(img, exif.Orientation(bytes.NewReader(all)))
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil, "", fmt.Errorf("empty image")
	}
	scale := 1.0
	long := w
	if h > w {
		long = h
	}
	if long > MaxThumbnailDim {
		scale = float64(MaxThumbnailDim) / float64(long)
	}
	dstW := int(float64(w) * scale)
	dstH := int(float64(h) * scale)
	if dstW <= 0 {
		dstW = 1
	}
	if dstH <= 0 {
		dstH = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	// CatmullRom = high-quality bicubic. For ~12 MP → 512 px the
	// extra cost over BiLinear is negligible (single-digit ms) and
	// the result reads notably crisper on small list rows.
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	buf := new(bytes.Buffer)
	if err := jpeg.Encode(buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, "", fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), ".jpg", nil
}

// ImportImageThumbnail generates and persists a downscaled thumbnail
// for an image item whose blob already lives in the filestore. The
// blob is read from disk via `fs.Path(item.StorePath)`, downscaled,
// and written under `thumbnails/<id>.jpg`. The item's thumbnail_path
// column is updated in the same transaction. Idempotent — re-running
// replaces the existing thumbnail.
func ImportImageThumbnail(
	ctx context.Context,
	s store.Store,
	fs *filestore.FileStore,
	item *model.Item,
) (string, error) {
	if item.StorePath == "" {
		return "", fmt.Errorf("item %s has no blob", item.ID)
	}
	abs := fs.Path(item.StorePath)
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("blob missing: %w", err)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open blob: %w", err)
	}
	defer f.Close()
	thumb, ext, err := GenerateImageThumbnail(f)
	if err != nil {
		return "", err
	}
	return writeThumbnail(ctx, s, fs, item, bytes.NewReader(thumb), ext)
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

// applyOrientation rotates / flips img per the EXIF orientation
// value so the returned image is in upright display order. Pixels
// are copied via golang.org/x/image/draw — the same backend the
// resize step uses — so there's no extra dependency. orientation
// 1 (default / no transform) returns the input unchanged.
func applyOrientation(img image.Image, orientation int) image.Image {
	if orientation == 1 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	// Output dimensions swap for 90/270-degree rotations.
	var dst *image.NRGBA
	switch orientation {
	case 5, 6, 7, 8:
		dst = image.NewNRGBA(image.Rect(0, 0, h, w))
	default:
		dst = image.NewNRGBA(image.Rect(0, 0, w, h))
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.At(b.Min.X+x, b.Min.Y+y)
			var dx, dy int
			switch orientation {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // mirror horizontal + rotate 270 CW
				dx, dy = y, x
			case 6: // rotate 90 CW
				dx, dy = h-1-y, x
			case 7: // mirror horizontal + rotate 90 CW
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 270 CW (= 90 CCW)
				dx, dy = y, w-1-x
			default:
				dx, dy = x, y
			}
			dst.Set(dx, dy, c)
		}
	}
	return dst
}
