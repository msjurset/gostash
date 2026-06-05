package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/exif"
	"github.com/msjurset/gostash/internal/extract"
	"github.com/msjurset/gostash/internal/fetch"
	"github.com/msjurset/gostash/internal/langdetect"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/rules"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add <url|file|dir|->",
	Short: "Stash a URL, file, directory, or stdin snippet",
	Long: `Add content to your stash. The source is auto-detected:

  stash add https://example.com     # bookmark a URL
  stash add ./document.pdf          # store a file
  stash add ./myproject/            # tar.gz and store a directory
  echo "note" | stash add -         # capture stdin as snippet
  stash add -                       # read from piped stdin`,
	Args: cobra.ExactArgs(1),
	RunE: runAdd,
}

func init() {
	addCmd.Flags().StringP("title", "t", "", "Title (auto-detected if omitted)")
	addCmd.Flags().StringSliceP("tag", "T", nil, "Tags (repeatable)")
	addCmd.Flags().StringP("note", "n", "", "Note to attach")
	addCmd.Flags().StringP("collection", "c", "", "Add to collection")
	addCmd.Flags().String("type", "", "Force type (url, snippet, file, image, email)")
	addCmd.Flags().StringP("extracted-text", "e", "",
		"Extracted text body. Use @filename to read from a file. Overrides any value derived from the source.")
	addCmd.Flags().BoolP("delete", "d", false, "Delete source file/directory after successful stash")
	addCmd.Flags().Bool("transcribe", false, "Transcribe video using Gemini (adds needs-identify tag)")
	rootCmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	fs := openFileStore()

	ctx := context.Background()
	source := args[0]

	title, _ := cmd.Flags().GetString("title")
	tags, _ := cmd.Flags().GetStringSlice("tag")
	note, _ := cmd.Flags().GetString("note")
	collection, _ := cmd.Flags().GetString("collection")
	forceType, _ := cmd.Flags().GetString("type")
	extractedTextFlag, _ := cmd.Flags().GetString("extracted-text")
	deleteSource, _ := cmd.Flags().GetBool("delete")
	transcribe, _ := cmd.Flags().GetBool("transcribe")

	// Resolve --extracted-text: a leading "@" reads from a file
	// (handles transcripts too long / too special-character-laden
	// to embed via shell expansion). Empty flag = leave whatever
	// the source-processing path set.
	extractedTextOverride, err := resolveExtractedTextFlag(extractedTextFlag)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	id := ulid.MustNew(ulid.Timestamp(now), entropy).String()

	item := &model.Item{
		ID:        id,
		Notes:     note,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  json.RawMessage("{}"),
	}

	// Build tags
	for _, t := range tags {
		item.Tags = append(item.Tags, model.Tag{Name: t})
	}

	// Track whether source is a file/dir eligible for deletion
	isFileSource := false

	opts := extract.Options{
		TranscribeVideo: transcribe,
	}

	switch {
	case source == "-" || isStdin():
		if err := addSnippet(item, fs, source); err != nil {
			LogCaptureError("stdin snippet", err.Error())
			return err
		}
	case isURL(source):
		if err := addLink(item, fs, source, opts); err != nil {
			LogCaptureError(source, err.Error())
			return err
		}
	case isDir(source):
		if err := addDirectory(item, fs, source); err != nil {
			LogCaptureError(source, err.Error())
			return err
		}
		isFileSource = true
	default:
		if err := addFile(item, fs, source, opts); err != nil {
			LogCaptureError(source, err.Error())
			return err
		}
		isFileSource = true
	}

	// Override type if forced
	if forceType != "" {
		item.Type = model.ParseItemType(forceType)
	}

	// Override extracted_text if the user passed --extracted-text.
	// Two-pointer wrapper so nil means "no override" and an empty
	// string means "clear the field." Recorder-style imports use
	// this to fold the .txt transcript into the same item as the
	// .m4a in one call.
	if extractedTextOverride != nil {
		item.ExtractedText = *extractedTextOverride
	}

	// Override title if provided
	if title != "" {
		item.Title = title
	}
	if item.Title == "" {
		item.Title = inferTitle(source, item.Type)
	}

	// Add auto-suggested tags from MIME type
	for _, st := range extract.SuggestTags(item.MimeType) {
		if !item.HasTag(st) {
			item.Tags = append(item.Tags, model.Tag{Name: st})
		}
	}

	// Set collection if specified
	if collection != "" {
		item.Collections = append(item.Collections, model.Collection{Name: collection})
	}

	// Apply user-defined capture rules. Pre-save effects (tags, collection,
	// title, note) fold into `item`; skip aborts the add; post-save effects
	// (link_to, notify) run after CreateItem succeeds. Same helper is used
	// by the chrome-host capture path so all sources behave identically.
	ruleResult := ApplyRulesToItem(s, item, RuleApplyContext{
		UserTitle:      title,
		UserNote:       note,
		UserCollection: collection,
	})
	if ruleResult.Skipped {
		logSkipped(item, ruleResult)
		for _, msg := range ruleResult.Notifies {
			fireNotification(item, msg)
		}
		if flagJSON {
			printJSON(map[string]any{
				"skipped":    true,
				"rule":       ruleResult.SkippedBy,
				"item_title": item.Title,
			})
		} else {
			fmt.Printf("Skipped by rule %q: %s\n", ruleResult.SkippedBy, item.Title)
		}
		return nil
	}
	EnsureRuleCollections(ctx, s, ruleResult)

	// Content-hash short-circuit: when the freshly-hashed bytes are
	// byte-identical to an item already in stash, skip creating a
	// duplicate row. Instead, merge any new tags / collections onto
	// the existing item. Without this, retried captures (e.g. the
	// user re-downloading from Google Photos when the extension's
	// confirm popup got dropped) pile up as `dup, dup-of-<id>`
	// entries — each one running its own redundant Gemini identify
	// pass because the daemon's worker sees each new row separately.
	// Skipping at ingest time keeps the item list clean AND avoids
	// the wasted spend. Image / file items only; URL / snippet
	// captures don't have content_hash populated.
	if item.ContentHash != "" {
		if existing, lookupErr := s.GetItemByContentHash(ctx, item.ContentHash); lookupErr == nil && existing != nil {
			added := mergeTagsAndCollections(ctx, s, existing, item)
			logDuplicateSkip(item, existing, added, ruleResult)
			if isFileSource && deleteSource {
				_ = os.RemoveAll(source)
			}
			return nil
		}
	}

	if err := s.CreateItem(ctx, item); err != nil {
		LogCaptureError(sourceFor(item), err.Error())
		return fmt.Errorf("save item: %w", err)
	}

	logRuleFire(item, ruleResult)
	logCapture(item, ruleResult)
	FirePostSaveRuleEffects(ctx, s, item, ruleResult)

	if flagJSON {
		printJSON(item)
	} else {
		fmt.Printf("Stashed %s [%s] %s\n", item.Type.Display(), shortID(item.ID), item.Title)
	}

	if deleteSource && isFileSource {
		if err := os.RemoveAll(source); err != nil {
			return fmt.Errorf("delete source: %w", err)
		}
		if !flagJSON {
			fmt.Printf("Deleted %s\n", source)
		}
	}

	return nil
}

func addSnippet(item *model.Item, fs interface{ Save(io.Reader) (string, int64, error) }, source string) error {
	var r io.Reader
	if source == "-" {
		r = os.Stdin
	} else {
		r = os.Stdin
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("empty input")
	}

	item.Type = model.TypeSnippet
	item.ExtractedText = string(data)
	item.MimeType = "text/plain"
	item.FileSize = int64(len(data))
	// Snippets have no separate capture moment — the row's
	// creation IS the capture. Set CapturedAt = CreatedAt
	// explicitly so consumers like Moments clustering treat
	// snippets uniformly with image/file items instead of
	// silently falling back to created_at.
	captured := item.CreatedAt
	item.CapturedAt = &captured

	// Snippets get a content_hash too so dedup works for clipboard
	// captures of the same string. The filestore is content-
	// addressable so the same bytes only land on disk once.
	if hash, _, err := fs.Save(bytes.NewReader(data)); err == nil {
		item.ContentHash = hash
		item.StorePath = hash
	}

	if lang := langdetect.Detect(string(data)); lang != "" {
		item.Metadata = json.RawMessage(fmt.Sprintf(`{"language":%q}`, lang))
	}
	return nil
}

func addLink(item *model.Item, fs interface{ Save(io.Reader) (string, int64, error) }, rawURL string, opts extract.Options) error {
	item.Type = model.TypeURL
	// Apply URL-exclusion rules from config.toml. The original URL
	// is still used for the fetch below; only what's persisted on
	// the item's URL column gets redacted, so the user can still
	// see "this came from <domain>" without re-visit-never session
	// noise in the stored URL.
	storedURL, _ := config.RedactURL(rawURL)
	item.URL = storedURL

	result, err := fetch.URL(rawURL)
	if err != nil {
		// Store the link even if fetch fails
		fmt.Fprintf(os.Stderr, "warning: fetch failed: %v (storing link anyway)\n", err)
		return nil
	}

	item.Title = result.Title
	item.ExtractedText = result.ExtractedText
	item.MimeType = result.MimeType

	// Save HTML snapshot
	if len(result.Body) > 0 {
		hash, size, err := fs.Save(bytes.NewReader(result.Body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: save snapshot failed: %v\n", err)
		} else {
			item.ContentHash = hash
			item.StorePath = hash
			item.FileSize = size
		}
	}
	return nil
}

func addFile(item *model.Item, fs interface{ Save(io.Reader) (string, int64, error) }, path string, opts extract.Options) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	// Read first 512 bytes for MIME detection
	header := make([]byte, 512)
	n, _ := f.Read(header)
	header = header[:n]
	f.Seek(0, io.SeekStart)

	mimeType := extract.DetectMIME(header, filepath.Base(absPath))
	item.MimeType = mimeType
	item.SourcePath = absPath

	switch {
	case mimeType == extract.MIMEEmail:
		item.Type = model.TypeEmail
	case strings.HasPrefix(mimeType, "image/"):
		item.Type = model.TypeImage
	default:
		item.Type = model.TypeFile
	}

	// Save to content-addressable store
	hash, size, err := fs.Save(f)
	if err != nil {
		return fmt.Errorf("store file: %w", err)
	}
	item.ContentHash = hash
	item.StorePath = hash
	item.FileSize = size

	// Extract text if possible
	stored, err := os.Open(absPath)
	if err == nil {
		defer stored.Close()
		result, err := extract.Run(stored, mimeType, opts)
		if err == nil {
			item.ExtractedText = result.Text
			if result.Title != "" && item.Title == "" {
				item.Title = result.Title
			}
			// Email extractor surfaces the most recent message
			// timestamp from the headers as CapturedAt; other
			// extractors leave it nil. Honored here before EXIF /
			// filesystem-time fallbacks since the header is
			// authoritative for emails.
			if result.CapturedAt != nil {
				t := result.CapturedAt.UTC()
				item.CapturedAt = &t
			}
			// Add tags from extractor
			for _, tag := range result.Tags {
				if !item.HasTag(tag) {
					item.Tags = append(item.Tags, model.Tag{Name: tag})
				}
			}
		}
	}

	// EXIF — image-only, best-effort. Mirrors the same extraction
	// stash serve's POST /capture does via internal/stash.populateFile.
	// Was historically missing from this CLI path (the two ingest
	// paths drifted), so files entering via `stash add` — which
	// includes everything sortie ingests — landed without
	// captured_at / location / camera metadata until a manual
	// `stash backfill-*` run. Now we extract inline. "No GPS" /
	// "no capture time" stay silent (expected for many images);
	// anything else gets logged so a future silent skip can't hide.
	if item.Type == model.TypeImage {
		exifLog := func(field string, err error) {
			log.Printf("[ingest] EXIF %s read failed for %s: %v", field, absPath, err)
		}
		if exifFile, openErr := os.Open(absPath); openErr == nil {
			if lat, lon, gpsErr := exif.ExtractGPS(exifFile); gpsErr == nil {
				item.Location = &model.Location{
					Lat: lat, Lon: lon, Source: "exif",
				}
			} else if !errors.Is(gpsErr, exif.ErrNoGPS) {
				exifLog("GPS", gpsErr)
			}
			exifFile.Close()
		} else {
			exifLog("GPS open", openErr)
		}
		if exifFile, openErr := os.Open(absPath); openErr == nil {
			if captured, ctErr := exif.ExtractCaptureTime(exifFile); ctErr == nil {
				t := captured.UTC()
				item.CapturedAt = &t
			} else if !errors.Is(ctErr, exif.ErrNoCaptureTime) {
				exifLog("CaptureTime", ctErr)
			}
			exifFile.Close()
		} else {
			exifLog("CaptureTime open", openErr)
		}
		if exifFile, openErr := os.Open(absPath); openErr == nil {
			if cam, ccErr := exif.ExtractCamera(exifFile); ccErr == nil {
				if cam.HasAny() {
					item.Metadata = mergeCameraMetadataAdd(item.Metadata, cam)
				}
			} else {
				exifLog("Camera", ccErr)
			}
			exifFile.Close()
		} else {
			exifLog("Camera open", openErr)
		}
	}

	// Filesystem-time fallback. For images, only kick in when EXIF
	// didn't surface a capture time; for arbitrary files, this IS
	// the capture signal (no better option exists). Mirrors the
	// same fallback in internal/stash.populateFile.
	if item.CapturedAt == nil &&
		(item.Type == model.TypeImage || item.Type == model.TypeFile) {
		if info, statErr := os.Stat(absPath); statErr == nil {
			t := info.ModTime().UTC()
			item.CapturedAt = &t
		}
	}

	return nil
}

// mergeCameraMetadataAdd merges a Camera struct into an item's
// metadata JSON under the "camera" key, preserving any other keys
// already present. Mirrors internal/stash.mergeCameraMetadata — same
// on-disk schema regardless of which ingest path produced the row.
// Kept package-local to avoid pulling internal/stash just for this
// helper.
func mergeCameraMetadataAdd(existing json.RawMessage, cam exif.Camera) json.RawMessage {
	m := map[string]any{}
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &m)
	}
	m["camera"] = cam
	out, err := json.Marshal(m)
	if err != nil {
		return existing
	}
	return out
}

func addDirectory(item *model.Item, fs interface{ Save(io.Reader) (string, int64, error) }, path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// Create tar.gz in a temp file
	tmp, err := os.CreateTemp("", "stash-dir-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	gw := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gw)

	baseDir := filepath.Base(absPath)
	err = filepath.Walk(absPath, func(file string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(absPath, file)
		if err != nil {
			return err
		}
		name := filepath.Join(baseDir, rel)

		header, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(name)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if fi.IsDir() {
			return nil
		}
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		tw.Close()
		gw.Close()
		tmp.Close()
		return fmt.Errorf("create archive: %w", err)
	}

	if err := tw.Close(); err != nil {
		gw.Close()
		tmp.Close()
		return fmt.Errorf("close tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		tmp.Close()
		return fmt.Errorf("close gzip: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Store the archive
	archiveFile, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("reopen archive: %w", err)
	}
	defer archiveFile.Close()

	hash, size, err := fs.Save(archiveFile)
	if err != nil {
		return fmt.Errorf("store archive: %w", err)
	}

	item.Type = model.TypeFile
	item.MimeType = "application/gzip"
	item.SourcePath = absPath
	item.ContentHash = hash
	item.StorePath = hash
	item.FileSize = size

	return nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func isURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}

func isStdin() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) == 0
}

func inferTitle(source string, itemType model.ItemType) string {
	switch itemType {
	case model.TypeURL:
		return titleFromURL(source)
	case model.TypeFile, model.TypeImage, model.TypeEmail:
		return filepath.Base(source)
	default:
		return "Untitled snippet"
	}
}

// titleFromURL extracts a human-readable title from a URL path.
// For example, "https://example.com/us-senate-confirms-judge-pick-2026-03-17/"
// becomes "Us Senate Confirms Judge Pick".
func titleFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	// Take the last non-empty path segment
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	slug := ""
	for i := len(segments) - 1; i >= 0; i-- {
		if segments[i] != "" {
			slug = segments[i]
			break
		}
	}
	if slug == "" {
		// Root URL like https://example.com — use the hostname
		return u.Host
	}

	// Strip common file extensions
	slug = strings.TrimSuffix(slug, ".html")
	slug = strings.TrimSuffix(slug, ".htm")

	// Replace separators with spaces
	slug = strings.NewReplacer("-", " ", "_", " ").Replace(slug)

	// Remove trailing date-like segments (e.g. "2026 03 17")
	words := strings.Fields(slug)
	for len(words) > 0 {
		w := words[len(words)-1]
		if len(w) <= 4 && isNumeric(w) {
			words = words[:len(words)-1]
		} else {
			break
		}
	}
	if len(words) == 0 {
		return u.Host
	}

	// Title-case each word
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

func hasCollection(cs []model.Collection, name string) bool {
	for _, c := range cs {
		if c.Name == name {
			return true
		}
	}
	return false
}

// mergeTagsAndCollections folds any tags / collections present on
// `incoming` (the newly-ingested item we're choosing NOT to create)
// onto `existing` (the item already in stash that shares the same
// content_hash). Returns the human-readable list of tags actually
// added — collections are added too but aren't surfaced in the
// terminal output to keep it scannable. Both AddTag and
// AddToCollection are idempotent server-side, so a partially-added
// state is safe to retry.
func mergeTagsAndCollections(
	ctx context.Context,
	s addStore,
	existing, incoming *model.Item,
) []string {
	added := []string{}
	for _, t := range incoming.Tags {
		// Drop dup-marker tags — they were auto-applied to the
		// incoming row by the rule engine BEFORE our short-circuit
		// fired, and only make sense on the would-be-created
		// duplicate row. Merging them onto the original gives the
		// surviving item a useless self-reference (`dup-of-<self>`)
		// and pollutes its tag list. Filter both the bare `dup`
		// flag and any `dup-of-…` pointer at the same time.
		if t.Name == "dup" || strings.HasPrefix(t.Name, "dup-of-") {
			continue
		}
		if existing.HasTag(t.Name) {
			continue
		}
		if err := s.AddTag(ctx, existing.ID, t.Name); err != nil {
			continue
		}
		added = append(added, t.Name)
	}
	for _, c := range incoming.Collections {
		if hasCollection(existing.Collections, c.Name) {
			continue
		}
		_ = s.AddToCollection(ctx, existing.ID, c.Name)
	}
	return added
}

// addStore is the subset of store.Store that the duplicate-merge
// path needs. Defined locally so this helper isn't coupled to the
// full store interface — and so tests can stub it cheaply.
type addStore interface {
	AddTag(ctx context.Context, itemID, tag string) error
	AddToCollection(ctx context.Context, itemID, collection string) error
}

// logDuplicateSkip surfaces a content-hash hit on the terminal +
// JSON output. The existing item's ID is the load-bearing piece
// — sortie's exec wrapper (and any user piping `stash add --json`)
// can read it back and chain further commands the same way they
// would for a fresh add.
func logDuplicateSkip(
	incoming, existing *model.Item,
	addedTags []string,
	ruleResult rules.Result,
) {
	if flagJSON {
		printJSON(map[string]any{
			"id":                existing.ID,
			"title":             existing.Title,
			"type":              existing.Type,
			"skipped_duplicate": true,
			"tags_added":        addedTags,
		})
		return
	}
	if len(addedTags) > 0 {
		fmt.Printf("Already stashed as [%s] %s — added tags: %s\n",
			existing.ID, existing.Title, strings.Join(addedTags, ", "))
	} else {
		fmt.Printf("Already stashed as [%s] %s — no changes\n",
			existing.ID, existing.Title)
	}
	// Run any post-fire rule notifications (e.g. notify-on-duplicate)
	// the same way fresh adds do, so a "you just re-stashed the same
	// image" desktop notification surfaces regardless of which code
	// path saved the work.
	for _, msg := range ruleResult.Notifies {
		fireNotification(existing, msg)
	}
}

// resolveExtractedTextFlag returns nil when the user didn't pass
// the flag, a pointer to an empty string when they passed "" (explicit
// clear), and otherwise the body. Leading "@" treats the rest as a
// filename to read from — needed for long transcripts or ones with
// shell-special characters that would mangle in command-line expansion.
// A literal "@" prefix can be escaped by doubling it (`@@…`).
func resolveExtractedTextFlag(raw string) (*string, error) {
	// Distinguishing "flag absent" vs "flag set to empty" via cobra
	// is awkward — cobra returns "" for both. Treat "" as "no
	// override" since clearing extracted_text on add doesn't make
	// sense (the field starts empty anyway). Users who really want
	// to wipe extracted_text post-add can use `stash edit -e ""`.
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "@@") {
		// Escape: literal text starting with @.
		s := raw[1:]
		return &s, nil
	}
	if strings.HasPrefix(raw, "@") {
		path := raw[1:]
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read --extracted-text file %q: %w", path, err)
		}
		s := string(data)
		return &s, nil
	}
	return &raw, nil
}
