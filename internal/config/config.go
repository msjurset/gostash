package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// Config holds all configurable paths.
type Config struct {
	DataDir     string `toml:"data_dir"`
	DBPath      string `toml:"db_path"`
	FilesDir    string `toml:"files_dir"`
	ImageViewer string `toml:"image_viewer"`
}

var (
	cfg     Config
	cfgOnce sync.Once
)

func configDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".config/stash"
	}
	return filepath.Join(home, ".config", "stash")
}

func configPath() string {
	return filepath.Join(configDir(), "config.toml")
}

func defaultDataDir() string {
	if d := os.Getenv("STASH_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".stash"
	}
	return filepath.Join(home, ".stash")
}

func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}

func load() Config {
	c := Config{}
	if data, err := os.ReadFile(configPath()); err == nil {
		toml.Unmarshal(data, &c)
	}

	// Expand ~ in all paths
	c.DataDir = expandHome(c.DataDir)
	c.DBPath = expandHome(c.DBPath)
	c.FilesDir = expandHome(c.FilesDir)

	// Env var overrides config file for data_dir
	if d := os.Getenv("STASH_DIR"); d != "" {
		c.DataDir = d
	}

	// Apply defaults for anything not set
	if c.DataDir == "" {
		c.DataDir = defaultDataDir()
	}
	if c.DBPath == "" {
		c.DBPath = filepath.Join(c.DataDir, "stash.db")
	}
	if c.FilesDir == "" {
		c.FilesDir = filepath.Join(c.DataDir, "files")
	}

	return c
}

// Get returns the loaded configuration, reading from disk on first call.
func Get() Config {
	cfgOnce.Do(func() { cfg = load() })
	return cfg
}

// Dir returns the stash data directory.
func Dir() string {
	return Get().DataDir
}

// DBPath returns the path to the SQLite database.
func DBPath() string {
	return Get().DBPath
}

// FilesDir returns the path to the content-addressable file store.
func FilesDir() string {
	return Get().FilesDir
}

// EnsureDir creates the stash data directory if it doesn't exist.
func EnsureDir() error {
	return os.MkdirAll(Dir(), 0755)
}

// WriteDefault writes a default config file if one doesn't exist.
func WriteDefault() error {
	path := configPath()
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}
	if err := os.MkdirAll(configDir(), 0755); err != nil {
		return err
	}
	content := `# Stash configuration
# data_dir = "~/.stash"
# db_path  = "~/.stash/stash.db"
# files_dir = "~/.stash/files"
# image_viewer = ""
`
	return os.WriteFile(path, []byte(content), 0644)
}
