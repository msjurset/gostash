// Package rules applies user-defined rules to a stashed item, performing
// actions like adding tags, assigning a collection, overriding the title,
// appending notes, sending notifications, linking to other items, or
// dropping the item entirely. Rules live in $STASH_DIR/rules.yaml.
//
// Match conditions on a single rule are AND-composed. Each rule has an
// `actions:` list whose items can bundle multiple effects (for the rare
// case where ordering within a rule matters). Across rules:
//
//   - tag additions are additive and deduped
//   - collection / title / note replacements are first-match-wins
//   - notes appended via `append_note` stack (newline-separated)
//   - `notify` calls stack
//   - `skip: true` from any matched rule aborts the add entirely
//
// Templates inside string actions use Go's text/template syntax with a
// preset data context — see TemplateData below for the available variables.
//
// User-supplied input on `stash add` (-T tags, -c collection, -t title,
// -n note) takes precedence over rule output: the caller is responsible
// for short-circuiting collection/title overrides when the user passed
// those flags explicitly.
package rules

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/msjurset/gostash/internal/model"
	"gopkg.in/yaml.v3"
)

// Match holds the conditions for a single rule. All set conditions must be
// true for the rule to fire (AND semantics). Empty conditions are skipped.
type Match struct {
	Type           string `yaml:"type,omitempty" json:"type,omitempty"`
	Domain         string `yaml:"domain,omitempty" json:"domain,omitempty"`
	URLRegex       string `yaml:"url_regex,omitempty" json:"url_regex,omitempty"`
	MimeType       string `yaml:"mime_type,omitempty" json:"mime_type,omitempty"`
	MimeTypePrefix string `yaml:"mime_type_prefix,omitempty" json:"mime_type_prefix,omitempty"`
	Sender         string `yaml:"sender,omitempty" json:"sender,omitempty"`
	SenderDomain   string `yaml:"sender_domain,omitempty" json:"sender_domain,omitempty"`
	PathGlob       string `yaml:"path_glob,omitempty" json:"path_glob,omitempty"`
	Content        string `yaml:"content,omitempty" json:"content,omitempty"`
	ContentRegex   string `yaml:"content_regex,omitempty" json:"content_regex,omitempty"`
}

// Action is one entry in a rule's `actions:` list. Any subset of fields may
// be set; whichever fields are populated take effect. Most rules will use
// a single field per Action; bundling multiple is allowed but rarely
// needed (the engine collects effects across the whole list).
//
// `omitempty` on every field is what produces the natural single-key map
// shape in YAML / JSON output:
//
//	actions:
//	  - add_tags: [video, watch-later]
//	  - notify: "New video: {{.Title}}"
type Action struct {
	AddTags       []string  `yaml:"add_tags,omitempty" json:"add_tags,omitempty"`
	AddCollection string    `yaml:"add_collection,omitempty" json:"add_collection,omitempty"`
	SetTitle      string    `yaml:"set_title,omitempty" json:"set_title,omitempty"`
	SetNote       string    `yaml:"set_note,omitempty" json:"set_note,omitempty"`
	AppendNote    string    `yaml:"append_note,omitempty" json:"append_note,omitempty"`
	Skip          bool      `yaml:"skip,omitempty" json:"skip,omitempty"`
	Notify        string    `yaml:"notify,omitempty" json:"notify,omitempty"`
	LinkTo        *LinkSpec `yaml:"link_to,omitempty" json:"link_to,omitempty"`
}

// LinkSpec selects link targets. Exactly one of Tag / ID should be set;
// behavior with both set is unspecified (Tag wins in current impl).
type LinkSpec struct {
	Tag string `yaml:"tag,omitempty" json:"tag,omitempty"`
	ID  string `yaml:"id,omitempty" json:"id,omitempty"`
}

// Rule is a single rule definition. Disabled rules are loaded but not applied.
//
// `Description` is a free-form one-liner that travels with the rule for
// human reference. It's never validated against the rule's behavior, so
// it can drift if you change the match/actions without updating it.
// Treat it like a comment that survives YAML round-trips.
type Rule struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Enabled     *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Match       Match    `yaml:"match" json:"match"`
	Actions     []Action `yaml:"actions,omitempty" json:"actions,omitempty"`
}

// IsEnabled reports whether the rule should run. Rules without an explicit
// `enabled:` key default to true.
func (r *Rule) IsEnabled() bool {
	return r.Enabled == nil || *r.Enabled
}

// Ruleset is the deserialized form of a rules.yaml file.
type Ruleset struct {
	Rules []Rule `yaml:"rules" json:"rules"`
}

// DefaultPath returns the rules-file location: $STASH_DIR/rules.yaml.
func DefaultPath(stashDir string) string {
	return filepath.Join(stashDir, "rules.yaml")
}

// LegacyPath returns the previous file location used by the autotag
// feature this replaced. Load() silently migrates files at this path.
func LegacyPath(stashDir string) string {
	return filepath.Join(stashDir, "autotag.yaml")
}

// Load reads and parses the rules file. If `path` does not exist but the
// legacy autotag.yaml does, the file is renamed and its contents migrated
// to the new actions: list shape silently — a one-time data migration.
//
// Returns an empty (no-rules) ruleset and nil error if neither file
// exists. Parse / migration errors are returned.
func Load(path string) (*Ruleset, error) {
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		legacy := filepath.Join(filepath.Dir(path), "autotag.yaml")
		if _, err := os.Stat(legacy); err == nil {
			if err := migrateLegacy(legacy, path); err != nil {
				return nil, fmt.Errorf("migrate legacy autotag.yaml: %w", err)
			}
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Ruleset{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var rs Ruleset
	if err := yaml.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i := range rs.Rules {
		if rs.Rules[i].Name == "" {
			return nil, fmt.Errorf("%s: rule at index %d is missing a name", path, i)
		}
	}
	return &rs, nil
}

// Result captures the additions/replacements an Apply pass wants to make
// to an item. The CLI's add hook reads these and folds them into the
// item before persisting (or aborts on Skipped).
type Result struct {
	Tags         []string
	Collection   string
	Title        string
	Note         string // replacement note (set by set_note; first-match-wins)
	AppendedNote string // collected appended notes, newline-separated
	Skipped      bool
	SkippedBy    string // name of the rule that requested the skip
	Notifies     []string
	Links        []LinkSpec
	MatchedRules []string
	Errors       []error
}

// HasNoteUpdate reports whether either set_note or append_note ran.
func (r *Result) HasNoteUpdate() bool {
	return r.Note != "" || r.AppendedNote != ""
}

// MergedNote returns the note value to write, combining set_note (first
// match) with appended notes. Existing item notes are merged by the caller.
func (r *Result) MergedNote(existing string) string {
	parts := []string{}
	if existing != "" {
		parts = append(parts, existing)
	}
	if r.Note != "" {
		// set_note replaces existing entirely
		parts = []string{r.Note}
	}
	if r.AppendedNote != "" {
		parts = append(parts, r.AppendedNote)
	}
	return strings.Join(parts, "\n")
}

// Apply runs every enabled rule against the item and returns the
// composite result. Templates are rendered using TemplateData built from
// the item.
func (rs *Ruleset) Apply(item *model.Item) Result {
	var res Result
	if rs == nil {
		return res
	}

	existingTags := make(map[string]struct{}, len(item.Tags))
	for _, t := range item.Tags {
		existingTags[strings.ToLower(t.Name)] = struct{}{}
	}
	addedTags := make(map[string]struct{})

	for i := range rs.Rules {
		rule := &rs.Rules[i]
		if !rule.IsEnabled() {
			continue
		}
		ok, captures, err := rule.matches(item)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("rule %q: %w", rule.Name, err))
			continue
		}
		if !ok {
			continue
		}
		res.MatchedRules = append(res.MatchedRules, rule.Name)

		td := buildTemplateData(item, rule.Name, captures)

		for _, act := range rule.Actions {
			// Tag additions
			for _, tag := range act.AddTags {
				tag = strings.TrimSpace(tag)
				if tag == "" {
					continue
				}
				lower := strings.ToLower(tag)
				if _, dup := existingTags[lower]; dup {
					continue
				}
				if _, dup := addedTags[lower]; dup {
					continue
				}
				addedTags[lower] = struct{}{}
				res.Tags = append(res.Tags, tag)
			}
			if act.AddCollection != "" && res.Collection == "" {
				res.Collection = act.AddCollection
			}
			if act.SetTitle != "" && res.Title == "" {
				rendered, terr := renderTemplate(act.SetTitle, td)
				if terr != nil {
					res.Errors = append(res.Errors, fmt.Errorf("rule %q set_title: %w", rule.Name, terr))
				} else {
					res.Title = rendered
				}
			}
			if act.SetNote != "" && res.Note == "" {
				rendered, terr := renderTemplate(act.SetNote, td)
				if terr != nil {
					res.Errors = append(res.Errors, fmt.Errorf("rule %q set_note: %w", rule.Name, terr))
				} else {
					res.Note = rendered
				}
			}
			if act.AppendNote != "" {
				rendered, terr := renderTemplate(act.AppendNote, td)
				if terr != nil {
					res.Errors = append(res.Errors, fmt.Errorf("rule %q append_note: %w", rule.Name, terr))
				} else if rendered != "" {
					if res.AppendedNote == "" {
						res.AppendedNote = rendered
					} else {
						res.AppendedNote = res.AppendedNote + "\n" + rendered
					}
				}
			}
			if act.Notify != "" {
				rendered, terr := renderTemplate(act.Notify, td)
				if terr != nil {
					res.Errors = append(res.Errors, fmt.Errorf("rule %q notify: %w", rule.Name, terr))
				} else {
					res.Notifies = append(res.Notifies, rendered)
				}
			}
			if act.LinkTo != nil {
				res.Links = append(res.Links, *act.LinkTo)
			}
			if act.Skip {
				res.Skipped = true
				if res.SkippedBy == "" {
					res.SkippedBy = rule.Name
				}
			}
		}
	}

	return res
}

// matches evaluates every set condition on the rule. All non-empty
// conditions must hold. Returns:
//   - bool: whether the rule matched
//   - map: named regex captures from url_regex / content_regex (nil if none)
//   - error: regex compile failure (rule treated as non-matching)
func (r *Rule) matches(item *model.Item) (bool, map[string]string, error) {
	m := r.Match
	var captures map[string]string

	if m.Type != "" {
		want := model.ParseItemType(m.Type)
		if item.Type != want {
			return false, nil, nil
		}
	}

	if m.Domain != "" {
		host := urlHost(item.URL)
		if host == "" || !hostMatches(host, m.Domain) {
			return false, nil, nil
		}
	}

	if m.URLRegex != "" {
		re, err := regexp.Compile(m.URLRegex)
		if err != nil {
			return false, nil, fmt.Errorf("url_regex: %w", err)
		}
		match := re.FindStringSubmatch(item.URL)
		if match == nil {
			return false, nil, nil
		}
		captures = collectCaptures(captures, re, match)
	}

	if m.MimeType != "" {
		if !strings.EqualFold(item.MimeType, m.MimeType) {
			return false, nil, nil
		}
	}

	if m.MimeTypePrefix != "" {
		if !strings.HasPrefix(strings.ToLower(item.MimeType), strings.ToLower(m.MimeTypePrefix)) {
			return false, nil, nil
		}
	}

	if m.Sender != "" || m.SenderDomain != "" {
		from := emailSender(item)
		if from == "" {
			return false, nil, nil
		}
		if m.Sender != "" {
			if !strings.Contains(strings.ToLower(from), strings.ToLower(m.Sender)) {
				return false, nil, nil
			}
		}
		if m.SenderDomain != "" {
			dom := senderDomain(from)
			if dom == "" || !hostMatches(dom, m.SenderDomain) {
				return false, nil, nil
			}
		}
	}

	if m.PathGlob != "" {
		if item.SourcePath == "" {
			return false, nil, nil
		}
		matched, err := filepath.Match(m.PathGlob, item.SourcePath)
		if err != nil {
			return false, nil, fmt.Errorf("path_glob: %w", err)
		}
		if !matched {
			matched, err = filepath.Match(m.PathGlob, filepath.Base(item.SourcePath))
			if err != nil {
				return false, nil, fmt.Errorf("path_glob: %w", err)
			}
			if !matched {
				return false, nil, nil
			}
		}
	}

	if m.Content != "" {
		if !strings.Contains(strings.ToLower(item.ExtractedText), strings.ToLower(m.Content)) {
			return false, nil, nil
		}
	}

	if m.ContentRegex != "" {
		re, err := regexp.Compile(m.ContentRegex)
		if err != nil {
			return false, nil, fmt.Errorf("content_regex: %w", err)
		}
		match := re.FindStringSubmatch(item.ExtractedText)
		if match == nil {
			return false, nil, nil
		}
		captures = collectCaptures(captures, re, match)
	}

	if m == (Match{}) {
		return false, nil, nil
	}

	return true, captures, nil
}

// collectCaptures merges named groups from a regex match into `into`,
// allocating the map lazily. Unnamed groups are ignored. Later calls
// overwrite earlier values for duplicate names.
func collectCaptures(into map[string]string, re *regexp.Regexp, match []string) map[string]string {
	names := re.SubexpNames()
	for i, name := range names {
		if name == "" || i >= len(match) {
			continue
		}
		if into == nil {
			into = map[string]string{}
		}
		into[name] = match[i]
	}
	return into
}

// urlHost extracts the host portion of a URL, lowercased. Returns "" if
// the URL is empty or unparseable.
func urlHost(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// hostMatches reports whether `host` matches `pattern`. Case-insensitive,
// suffix-aware: pattern "youtube.com" matches "youtube.com" and
// "www.youtube.com" but not "notyoutube.com".
func hostMatches(host, pattern string) bool {
	host = strings.ToLower(host)
	pattern = strings.ToLower(pattern)
	if host == pattern {
		return true
	}
	return strings.HasSuffix(host, "."+pattern)
}

// emailSender extracts the From: header value from an email item's
// extracted text. Returns "" for non-email items or items without a
// From: line.
func emailSender(item *model.Item) string {
	if item.Type != model.TypeEmail {
		return ""
	}
	for _, line := range strings.Split(item.ExtractedText, "\n") {
		if strings.HasPrefix(line, "From: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "From: "))
		}
		if line == "" {
			break
		}
	}
	return ""
}

// senderEmail returns just the address portion of a From: value. Handles
// both `Name <user@domain>` and bare `user@domain`.
func senderEmail(from string) string {
	if lt := strings.LastIndex(from, "<"); lt >= 0 {
		if gt := strings.Index(from[lt:], ">"); gt > 0 {
			return strings.TrimSpace(from[lt+1 : lt+gt])
		}
	}
	return strings.TrimSpace(from)
}

// senderName returns the display-name portion of a From: value, or "" if
// the From: is just a bare email address.
func senderName(from string) string {
	lt := strings.LastIndex(from, "<")
	if lt <= 0 {
		return ""
	}
	name := strings.TrimSpace(from[:lt])
	name = strings.Trim(name, "\"")
	return name
}

// senderDomain pulls the domain portion out of a From: value.
func senderDomain(from string) string {
	addr := senderEmail(from)
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[at+1:]))
}

// emailSubject pulls the Subject: header from an email item's extracted
// text. Returns "" if absent.
func emailSubject(item *model.Item) string {
	if item.Type != model.TypeEmail {
		return ""
	}
	for _, line := range strings.Split(item.ExtractedText, "\n") {
		if strings.HasPrefix(line, "Subject: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Subject: "))
		}
		if line == "" {
			break
		}
	}
	return ""
}
