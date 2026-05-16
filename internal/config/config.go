package config

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// Config holds all configurable paths and capture-time rules.
type Config struct {
	DataDir     string      `toml:"data_dir"`
	DBPath      string      `toml:"db_path"`
	FilesDir    string      `toml:"files_dir"`
	BackupDir   string      `toml:"backup_dir"`
	ImageViewer string      `toml:"image_viewer"`
	Exclusions  []Exclusion `toml:"exclusions"`
}

// Exclusion redacts the URL field on items captured from matching
// sources, so transient / session-only URLs (Gemini chats, OAuth
// flows, Slack threads with auth tokens) don't pollute the stash
// with re-visits-never URLs. Capture still happens — only the URL
// field is rewritten.
type Exclusion struct {
	// Pattern: the literal string for `domain` matches (with an
	// optional `*.` prefix for suffix matching) or an RE2 pattern
	// for `regex` matches.
	Pattern string `toml:"pattern" json:"pattern"`
	// Match type: "domain" (default) or "regex".
	Match string `toml:"match,omitempty" json:"match,omitempty"`
	// Behavior on match: "clear" (drop the URL) or "domain" (keep
	// scheme + host, drop path / query / fragment). Default
	// "domain" — preserves "this came from gemini.google.com"
	// without the session-id noise.
	Behavior string `toml:"behavior,omitempty" json:"behavior,omitempty"`
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
	c.BackupDir = expandHome(c.BackupDir)

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
	if c.BackupDir == "" {
		c.BackupDir = filepath.Join(c.DataDir, "backups")
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

// BackupDir returns the path to the backup directory.
func BackupDir() string {
	return Get().BackupDir
}

// EnsureDir creates the stash data directory if it doesn't exist.
func EnsureDir() error {
	return os.MkdirAll(Dir(), 0755)
}

// RedactURL applies the configured exclusion rules to a freshly
// captured URL. Returns (newURL, true) when a rule matched and
// rewrote the URL; (rawURL, false) when no rule matched.
//
// "domain" behavior keeps `scheme://host[:port]/` and drops the
// path / query / fragment — useful for session URLs whose host
// is meaningful but whose path is transient.
//
// "clear" behavior returns the empty string — the caller should
// store no URL at all on the item.
func RedactURL(rawURL string) (string, bool) {
	c := Get()
	if len(c.Exclusions) == 0 || rawURL == "" {
		return rawURL, false
	}
	for _, ex := range c.Exclusions {
		if !ex.matches(rawURL) {
			continue
		}
		return ex.apply(rawURL), true
	}
	return rawURL, false
}

func (e Exclusion) matches(rawURL string) bool {
	switch strings.ToLower(e.Match) {
	case "regex":
		re, err := regexp.Compile(e.Pattern)
		if err != nil {
			return false
		}
		return re.MatchString(rawURL)
	default:
		// "domain" — exact host match or `*.suffix` wildcard.
		u, err := url.Parse(rawURL)
		if err != nil {
			return false
		}
		host := strings.ToLower(u.Hostname())
		pat := strings.ToLower(strings.TrimSpace(e.Pattern))
		if pat == "" {
			return false
		}
		if strings.HasPrefix(pat, "*.") {
			suffix := pat[1:] // ".example.com"
			return host == pat[2:] || strings.HasSuffix(host, suffix)
		}
		return host == pat
	}
}

func (e Exclusion) apply(rawURL string) string {
	switch strings.ToLower(e.Behavior) {
	case "clear":
		return ""
	default:
		// "domain" — preserve scheme + host[:port] only.
		u, err := url.Parse(rawURL)
		if err != nil || u.Host == "" {
			return ""
		}
		return u.Scheme + "://" + u.Host + "/"
	}
}

// Save writes the current Config back to disk as TOML. Used by the
// `stash config exclusions add/remove` subcommands; the Mac
// Settings sheet routes through those.
//
// **Comments are lost**: BurntSushi/toml's marshaler doesn't
// preserve them. The config file is mostly machine-edited via the
// Mac UI now, so this is acceptable — but flag it in the warning.
func Save(c Config) error {
	if err := os.MkdirAll(configDir(), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(configPath(), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := toml.NewEncoder(f)
	enc.Indent = ""
	return enc.Encode(&c)
}

// Reload forces the next Get() to re-read the file. Used after
// Save() so the in-process cached config doesn't lag the disk.
func Reload() {
	cfgOnce = sync.Once{}
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
# data_dir   = "~/.stash"
# db_path    = "~/.stash/stash.db"
# files_dir  = "~/.stash/files"
# backup_dir = "~/.stash/backups"
# image_viewer = ""
`
	return os.WriteFile(path, []byte(content), 0644)
}
