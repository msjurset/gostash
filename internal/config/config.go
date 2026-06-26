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

type OperationConfig struct {
	PrimaryModel string   `toml:"primary_model" json:"primary_model"`
	AIModels     []string `toml:"ai_models" json:"ai_models"`
}

// Config holds all configurable paths and capture-time rules.
type Config struct {
	DataDir     string      `toml:"data_dir" json:"data_dir"`
	DBPath      string      `toml:"db_path" json:"db_path"`
	FilesDir    string      `toml:"files_dir" json:"files_dir"`
	BackupDir   string      `toml:"backup_dir" json:"backup_dir"`
	ImageViewer string      `toml:"image_viewer" json:"image_viewer,omitempty"`
	Exclusions  []Exclusion `toml:"exclusions" json:"exclusions"`
	// 1Password reference for the Gemini API key. NOT the secret —
	// just the op://vault/item/field path. The secret itself lives
	// in the system keychain (see internal/credentials). Stored
	// here so `stash auth refresh-gemini` can re-resolve without
	// arguments — used by the deploy hook to re-prime the Keychain
	// ACL after the binary's cdhash changes.
	GeminiOpRef string `toml:"gemini_op_ref,omitempty" json:"gemini_op_ref,omitempty"`

	// Cost controls and AI model fallback configurations
	PrimaryModel            string                     `toml:"primary_model" json:"primary_model"`
	AIModels                []string                   `toml:"ai_models" json:"ai_models"`
	Operations              map[string]OperationConfig `toml:"operations" json:"operations"`
	MaxMonthlyBudgetUSD     float64                    `toml:"max_monthly_budget_usd" json:"max_monthly_budget_usd"`
	MaxDailyBudgetUSD       float64                    `toml:"max_daily_budget_usd" json:"max_daily_budget_usd"`
	MaxVideoDurationMinutes int                        `toml:"max_video_duration_minutes" json:"max_video_duration_minutes"`
	PaidTierEnabled         bool                       `toml:"paid_tier_enabled" json:"paid_tier_enabled"`
	PaidCredential          string                     `toml:"paid_credential" json:"-"`
	PaidApprovalDurationHours int                      `toml:"paid_approval_duration_hours" json:"paid_approval_duration_hours"`
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
	cfg       Config
	cfgLoaded bool
	cfgMu     sync.RWMutex
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
	c := Config{
		PaidTierEnabled: false,
	}
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

	if c.PrimaryModel == "" {
		c.PrimaryModel = "gemini-2.5-flash"
	}
	if len(c.AIModels) == 0 {
		c.AIModels = []string{"gemini-2.5-flash"}
	}
	if c.Operations == nil {
		c.Operations = make(map[string]OperationConfig)
	}
	if c.MaxVideoDurationMinutes <= 0 {
		c.MaxVideoDurationMinutes = 30
	}
	if c.PaidApprovalDurationHours <= 0 {
		c.PaidApprovalDurationHours = 24
	}

	return c
}

// Get returns the loaded configuration, reading from disk on first call.
func Get() Config {
	cfgMu.RLock()
	if cfgLoaded {
		c := cfg
		cfgMu.RUnlock()
		return c
	}
	cfgMu.RUnlock()

	cfgMu.Lock()
	defer cfgMu.Unlock()
	if !cfgLoaded {
		cfg = load()
		cfgLoaded = true
	}
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
	cfgMu.Lock()
	defer cfgMu.Unlock()

	if err := os.MkdirAll(configDir(), 0755); err != nil {
		return err
	}

	path := configPath()
	tmpFile, err := os.CreateTemp(configDir(), "config-*.toml")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpName)
	}()

	enc := toml.NewEncoder(tmpFile)
	enc.Indent = ""
	if err := enc.Encode(&c); err != nil {
		return err
	}

	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		return err
	}

	cfg = c
	cfgLoaded = true
	return nil
}

// Reload forces the next Get() to re-read the file. Used after
// Save() so the in-process cached config doesn't lag the disk.
func Reload() {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	cfg = load()
	cfgLoaded = true
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
