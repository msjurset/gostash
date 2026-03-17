package config

import (
	"os"
	"path/filepath"
)

const defaultDir = ".stash"

// Dir returns the stash data directory, defaulting to ~/.stash/.
// Override with STASH_DIR environment variable.
func Dir() string {
	if d := os.Getenv("STASH_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultDir
	}
	return filepath.Join(home, defaultDir)
}

// DBPath returns the path to the SQLite database.
func DBPath() string {
	return filepath.Join(Dir(), "stash.db")
}

// FilesDir returns the path to the content-addressable file store.
func FilesDir() string {
	return filepath.Join(Dir(), "files")
}

// EnsureDir creates the stash directory if it doesn't exist.
func EnsureDir() error {
	return os.MkdirAll(Dir(), 0755)
}
