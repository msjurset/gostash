package stash

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/msjurset/gostash/internal/extract"
	"github.com/msjurset/gostash/internal/model"

	"github.com/oklog/ulid/v2"
)

// FileStore is the interface needed for content-addressable storage.
type FileStore interface {
	Save(r io.Reader) (hash string, size int64, err error)
}

// Params holds parameters for stashing a file or directory.
type Params struct {
	Title        string
	Tags         []string
	Note         string
	Collection   string
	DeleteSource bool
}

// Result holds the outcome of a stash operation.
type Result struct {
	Item    *model.Item
	Deleted bool
}

// File stashes a single file and returns the created item.
func File(ctx context.Context, s interface{ CreateItem(context.Context, *model.Item) error }, fs FileStore, path string, p Params) (*Result, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	item := newItem(p)
	if err := populateFile(item, fs, absPath); err != nil {
		return nil, err
	}

	if p.Title != "" {
		item.Title = p.Title
	}
	if item.Title == "" {
		item.Title = filepath.Base(absPath)
	}

	addSuggestedTags(item)

	if err := s.CreateItem(ctx, item); err != nil {
		return nil, fmt.Errorf("save item: %w", err)
	}

	res := &Result{Item: item}
	if p.DeleteSource {
		if err := os.RemoveAll(absPath); err != nil {
			return res, fmt.Errorf("delete source: %w", err)
		}
		res.Deleted = true
	}
	return res, nil
}

// Directory stashes a single directory as a tar.gz archive.
func Directory(ctx context.Context, s interface{ CreateItem(context.Context, *model.Item) error }, fs FileStore, path string, p Params) (*Result, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	item := newItem(p)
	if err := populateDirectory(item, fs, absPath); err != nil {
		return nil, err
	}

	if p.Title != "" {
		item.Title = p.Title
	}
	if item.Title == "" {
		item.Title = filepath.Base(absPath)
	}

	addSuggestedTags(item)

	if err := s.CreateItem(ctx, item); err != nil {
		return nil, fmt.Errorf("save item: %w", err)
	}

	res := &Result{Item: item}
	if p.DeleteSource {
		if err := os.RemoveAll(absPath); err != nil {
			return res, fmt.Errorf("delete source: %w", err)
		}
		res.Deleted = true
	}
	return res, nil
}

// Archive stashes multiple paths (files and/or directories) as a single tar.gz.
func Archive(ctx context.Context, s interface{ CreateItem(context.Context, *model.Item) error }, fs FileStore, paths []string, p Params) (*Result, error) {
	item := newItem(p)

	tmp, err := os.CreateTemp("", "stash-archive-*.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	gw := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gw)

	for _, path := range paths {
		absPath, err := filepath.Abs(path)
		if err != nil {
			tw.Close()
			gw.Close()
			tmp.Close()
			return nil, fmt.Errorf("resolve path %s: %w", path, err)
		}

		fi, err := os.Stat(absPath)
		if err != nil {
			tw.Close()
			gw.Close()
			tmp.Close()
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}

		if fi.IsDir() {
			if err := addDirToTar(tw, absPath); err != nil {
				tw.Close()
				gw.Close()
				tmp.Close()
				return nil, err
			}
		} else {
			if err := addFileToTar(tw, absPath); err != nil {
				tw.Close()
				gw.Close()
				tmp.Close()
				return nil, err
			}
		}
	}

	if err := tw.Close(); err != nil {
		gw.Close()
		tmp.Close()
		return nil, fmt.Errorf("close tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("close gzip: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp: %w", err)
	}

	archiveFile, err := os.Open(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("reopen archive: %w", err)
	}
	defer archiveFile.Close()

	hash, size, err := fs.Save(archiveFile)
	if err != nil {
		return nil, fmt.Errorf("store archive: %w", err)
	}

	item.Type = model.TypeFile
	item.MimeType = "application/gzip"
	item.ContentHash = hash
	item.StorePath = hash
	item.FileSize = size

	if p.Title != "" {
		item.Title = p.Title
	}
	if item.Title == "" {
		item.Title = fmt.Sprintf("Archive (%d items)", len(paths))
	}

	addSuggestedTags(item)

	if err := s.CreateItem(ctx, item); err != nil {
		return nil, fmt.Errorf("save item: %w", err)
	}

	res := &Result{Item: item}
	if p.DeleteSource {
		for _, path := range paths {
			os.RemoveAll(path)
		}
		res.Deleted = true
	}
	return res, nil
}

func newItem(p Params) *model.Item {
	now := time.Now().UTC()
	entropy := ulid.Monotonic(rand.New(rand.NewSource(now.UnixNano())), 0)
	id := ulid.MustNew(ulid.Timestamp(now), entropy).String()

	item := &model.Item{
		ID:        id,
		Notes:     p.Note,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  json.RawMessage("{}"),
	}

	for _, t := range p.Tags {
		item.Tags = append(item.Tags, model.Tag{Name: t})
	}

	if p.Collection != "" {
		item.Collections = append(item.Collections, model.Collection{Name: p.Collection})
	}

	return item
}

func populateFile(item *model.Item, fs FileStore, absPath string) error {
	f, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

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

	hash, size, err := fs.Save(f)
	if err != nil {
		return fmt.Errorf("store file: %w", err)
	}
	item.ContentHash = hash
	item.StorePath = hash
	item.FileSize = size

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

func populateDirectory(item *model.Item, fs FileStore, absPath string) error {
	tmp, err := os.CreateTemp("", "stash-dir-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	gw := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gw)

	if err := addDirToTar(tw, absPath); err != nil {
		tw.Close()
		gw.Close()
		tmp.Close()
		return err
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
		return fmt.Errorf("close temp: %w", err)
	}

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

func addDirToTar(tw *tar.Writer, dirPath string) error {
	baseDir := filepath.Base(dirPath)
	return filepath.Walk(dirPath, func(file string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dirPath, file)
		if err != nil {
			return err
		}
		name := filepath.Join(baseDir, rel)

		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(name)

		if err := tw.WriteHeader(hdr); err != nil {
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
}

func addFileToTar(tw *tar.Writer, filePath string) error {
	fi, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(fi, "")
	if err != nil {
		return err
	}
	hdr.Name = filepath.Base(filePath)

	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func addSuggestedTags(item *model.Item) {
	for _, st := range extract.SuggestTags(item.MimeType) {
		found := false
		for _, t := range item.Tags {
			if t.Name == st {
				found = true
				break
			}
		}
		if !found {
			item.Tags = append(item.Tags, model.Tag{Name: st})
		}
	}
}
