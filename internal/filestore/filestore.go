package filestore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileStore provides content-addressable file storage using SHA-256 hashes.
// Files are stored as: baseDir/<first-2-chars>/<full-hash>
type FileStore struct {
	baseDir string
}

// New creates a FileStore rooted at the given directory.
func New(baseDir string) *FileStore {
	return &FileStore{baseDir: baseDir}
}

// Save reads from r, computes the SHA-256 hash, and stores the content.
// Returns the hex-encoded hash and file size.
func (fs *FileStore) Save(r io.Reader) (hash string, size int64, err error) {
	// Write to a temp file while computing hash
	tmp, err := os.CreateTemp(fs.baseDir, ".tmp-*")
	if err != nil {
		// Ensure baseDir exists and retry
		if mkErr := os.MkdirAll(fs.baseDir, 0755); mkErr != nil {
			return "", 0, fmt.Errorf("create base dir: %w", mkErr)
		}
		tmp, err = os.CreateTemp(fs.baseDir, ".tmp-*")
		if err != nil {
			return "", 0, fmt.Errorf("create temp file: %w", err)
		}
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	w := io.MultiWriter(tmp, h)

	size, err = io.Copy(w, r)
	if err != nil {
		tmp.Close()
		return "", 0, fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("close temp file: %w", err)
	}

	hash = hex.EncodeToString(h.Sum(nil))
	destDir := filepath.Join(fs.baseDir, hash[:2])
	destPath := filepath.Join(destDir, hash)

	// Already stored — dedup
	if _, err := os.Stat(destPath); err == nil {
		os.Remove(tmpPath)
		return hash, size, nil
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", 0, fmt.Errorf("create hash dir: %w", err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return "", 0, fmt.Errorf("move to store: %w", err)
	}
	return hash, size, nil
}

// Path returns the absolute filesystem path for a content hash.
func (fs *FileStore) Path(hash string) string {
	if len(hash) < 2 {
		return ""
	}
	return filepath.Join(fs.baseDir, hash[:2], hash)
}

// Open returns a reader for the content identified by hash.
func (fs *FileStore) Open(hash string) (io.ReadCloser, error) {
	f, err := os.Open(fs.Path(hash))
	if err != nil {
		return nil, fmt.Errorf("open stored file: %w", err)
	}
	return f, nil
}

// Delete removes a stored file by hash.
func (fs *FileStore) Delete(hash string) error {
	p := fs.Path(hash)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete stored file: %w", err)
	}
	return nil
}

// Exists checks whether a file with the given hash is stored.
func (fs *FileStore) Exists(hash string) bool {
	_, err := os.Stat(fs.Path(hash))
	return err == nil
}
