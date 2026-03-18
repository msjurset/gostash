package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/extract"
	"github.com/msjurset/gostash/internal/fetch"
	"github.com/msjurset/gostash/internal/model"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add <url|file|->",
	Short: "Stash a URL, file, or stdin snippet",
	Long: `Add content to your stash. The source is auto-detected:

  stash add https://example.com     # bookmark a URL
  stash add ./document.pdf          # store a file
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
	addCmd.Flags().String("type", "", "Force type (link, snippet, file, image, email)")
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

	// Determine source type and process
	switch {
	case source == "-" || isStdin():
		if err := addSnippet(item, source); err != nil {
			return err
		}
	case isURL(source):
		if err := addLink(item, fs, source); err != nil {
			return err
		}
	default:
		if err := addFile(item, fs, source); err != nil {
			return err
		}
	}

	// Override type if forced
	if forceType != "" {
		item.Type = model.ItemType(forceType)
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
		if !hasTag(item.Tags, st) {
			item.Tags = append(item.Tags, model.Tag{Name: st})
		}
	}

	// Set collection if specified
	if collection != "" {
		item.Collections = append(item.Collections, model.Collection{Name: collection})
	}

	if err := s.CreateItem(ctx, item); err != nil {
		return fmt.Errorf("save item: %w", err)
	}

	if flagJSON {
		printJSON(item)
	} else {
		fmt.Printf("Stashed %s [%s] %s\n", item.Type, shortID(item.ID), item.Title)
	}
	return nil
}

func addSnippet(item *model.Item, source string) error {
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
	return nil
}

func addLink(item *model.Item, fs interface{ Save(io.Reader) (string, int64, error) }, rawURL string) error {
	item.Type = model.TypeLink
	item.URL = rawURL

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

func addFile(item *model.Item, fs interface{ Save(io.Reader) (string, int64, error) }, path string) error {
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
		result, err := extract.Run(stored, mimeType)
		if err == nil {
			item.ExtractedText = result.Text
			if result.Title != "" && item.Title == "" {
				item.Title = result.Title
			}
		}
	}

	return nil
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
	case model.TypeLink:
		return source
	case model.TypeFile, model.TypeImage, model.TypeEmail:
		return filepath.Base(source)
	default:
		return "Untitled snippet"
	}
}

func hasTag(tags []model.Tag, name string) bool {
	for _, t := range tags {
		if t.Name == name {
			return true
		}
	}
	return false
}
