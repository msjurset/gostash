package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/msjurset/gostash/internal/model"
)

func emailItem(extractedText string) *model.Item {
	return &model.Item{
		Type:          model.TypeEmail,
		ExtractedText: extractedText,
	}
}

func urlItem(rawURL string) *model.Item {
	return &model.Item{
		Type: model.TypeURL,
		URL:  rawURL,
	}
}

func fileItem(path, mime, text string) *model.Item {
	return &model.Item{
		Type:          model.TypeFile,
		SourcePath:    path,
		MimeType:      mime,
		ExtractedText: text,
	}
}

func tagsAction(tags ...string) Action      { return Action{AddTags: tags} }
func collectionAction(name string) Action   { return Action{AddCollection: name} }

func TestMatch_Type(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "url-only", Match: Match{Type: "url"}, Actions: []Action{tagsAction("web")}},
	}}
	res := rs.Apply(urlItem("https://example.com"))
	if !contains(res.Tags, "web") {
		t.Errorf("url item: tags=%v, want [web]", res.Tags)
	}
	res = rs.Apply(fileItem("/tmp/x.pdf", "application/pdf", ""))
	if len(res.Tags) != 0 {
		t.Errorf("file item: tags=%v, want []", res.Tags)
	}
}

func TestMatch_Domain(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "yt", Match: Match{Domain: "youtube.com"}, Actions: []Action{tagsAction("video")}},
	}}
	cases := []struct {
		url  string
		want bool
	}{
		{"https://youtube.com/watch?v=1", true},
		{"https://www.youtube.com/watch?v=1", true},
		{"https://m.youtube.com/x", true},
		{"https://notyoutube.com/x", false},
		{"https://example.com", false},
	}
	for _, tc := range cases {
		res := rs.Apply(urlItem(tc.url))
		got := len(res.Tags) > 0
		if got != tc.want {
			t.Errorf("url=%q matched=%v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestComposability_AND(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "yt-watch", Match: Match{Domain: "youtube.com", URLRegex: `/watch`}, Actions: []Action{tagsAction("video")}},
	}}
	if res := rs.Apply(urlItem("https://youtube.com/watch?v=1")); !contains(res.Tags, "video") {
		t.Errorf("both conditions match, want tag: %v", res.Tags)
	}
	if res := rs.Apply(urlItem("https://youtube.com/feed")); contains(res.Tags, "video") {
		t.Errorf("only domain matches — should not fire: %v", res.Tags)
	}
}

func TestAllMatchingRulesContributeTags(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "a", Match: Match{Type: "url"}, Actions: []Action{tagsAction("web")}},
		{Name: "b", Match: Match{Domain: "youtube.com"}, Actions: []Action{tagsAction("video")}},
	}}
	res := rs.Apply(urlItem("https://youtube.com/x"))
	if !contains(res.Tags, "web") || !contains(res.Tags, "video") {
		t.Errorf("expected both web and video: %v", res.Tags)
	}
}

func TestFirstMatchWins_Collection(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "first", Match: Match{Type: "url"}, Actions: []Action{collectionAction("first-coll")}},
		{Name: "second", Match: Match{Type: "url"}, Actions: []Action{collectionAction("second-coll")}},
	}}
	res := rs.Apply(urlItem("https://example.com"))
	if res.Collection != "first-coll" {
		t.Errorf("collection=%q, want first-coll", res.Collection)
	}
}

func TestSetTitleFirstWins(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "first", Match: Match{Type: "url"}, Actions: []Action{{SetTitle: "First"}}},
		{Name: "second", Match: Match{Type: "url"}, Actions: []Action{{SetTitle: "Second"}}},
	}}
	res := rs.Apply(urlItem("https://example.com"))
	if res.Title != "First" {
		t.Errorf("title=%q, want First", res.Title)
	}
}

func TestAppendNoteStacks(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "a", Match: Match{Type: "url"}, Actions: []Action{{AppendNote: "note1"}}},
		{Name: "b", Match: Match{Type: "url"}, Actions: []Action{{AppendNote: "note2"}}},
	}}
	res := rs.Apply(urlItem("https://example.com"))
	if !strings.Contains(res.AppendedNote, "note1") || !strings.Contains(res.AppendedNote, "note2") {
		t.Errorf("expected both notes: %q", res.AppendedNote)
	}
	if !strings.Contains(res.AppendedNote, "\n") {
		t.Errorf("notes should be newline-separated: %q", res.AppendedNote)
	}
}

func TestSkipFlag(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "drop", Match: Match{Domain: "junk.example"}, Actions: []Action{{Skip: true}}},
		{Name: "tag",  Match: Match{Type: "url"},            Actions: []Action{tagsAction("web")}},
	}}
	res := rs.Apply(urlItem("https://junk.example/x"))
	if !res.Skipped {
		t.Errorf("expected skipped=true")
	}
	if res.SkippedBy != "drop" {
		t.Errorf("skipped_by=%q, want drop", res.SkippedBy)
	}
	res = rs.Apply(urlItem("https://other.example/x"))
	if res.Skipped {
		t.Errorf("non-junk should not skip")
	}
	if !contains(res.Tags, "web") {
		t.Errorf("non-junk should still tag")
	}
}

func TestNotifyStacks(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "n1", Match: Match{Type: "url"}, Actions: []Action{{Notify: "First"}}},
		{Name: "n2", Match: Match{Type: "url"}, Actions: []Action{{Notify: "Second"}}},
	}}
	res := rs.Apply(urlItem("https://example.com"))
	if len(res.Notifies) != 2 {
		t.Errorf("expected 2 notifies, got %d: %v", len(res.Notifies), res.Notifies)
	}
}

func TestLinkToCollected(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "l1", Match: Match{Type: "url"}, Actions: []Action{{LinkTo: &LinkSpec{Tag: "alpha"}}}},
		{Name: "l2", Match: Match{Type: "url"}, Actions: []Action{{LinkTo: &LinkSpec{ID: "01ABC"}}}},
	}}
	res := rs.Apply(urlItem("https://example.com"))
	if len(res.Links) != 2 {
		t.Fatalf("expected 2 link specs, got %d: %v", len(res.Links), res.Links)
	}
}

func TestExistingTagsDeduped(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "a", Match: Match{Type: "url"}, Actions: []Action{tagsAction("web", "WEB")}},
	}}
	item := &model.Item{Type: model.TypeURL, URL: "https://example.com",
		Tags: []model.Tag{{Name: "web"}}}
	res := rs.Apply(item)
	if len(res.Tags) != 0 {
		t.Errorf("existing tag should suppress add: tags=%v", res.Tags)
	}
}

func TestEnabledFlag(t *testing.T) {
	disabled := false
	rs := &Ruleset{Rules: []Rule{
		{Name: "off", Enabled: &disabled, Match: Match{Type: "url"}, Actions: []Action{tagsAction("x")}},
	}}
	res := rs.Apply(urlItem("https://example.com"))
	if len(res.Tags) != 0 {
		t.Errorf("disabled rule should not fire: tags=%v", res.Tags)
	}
}

func TestEmptyMatchDoesNotFire(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "broken", Match: Match{}, Actions: []Action{tagsAction("everywhere")}},
	}}
	res := rs.Apply(urlItem("https://example.com"))
	if len(res.Tags) != 0 {
		t.Errorf("empty match should NOT match anything: %v", res.Tags)
	}
}

func TestRegexCompileErrorReturned(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{Name: "bad", Match: Match{ContentRegex: "[invalid"}, Actions: []Action{tagsAction("x")}},
	}}
	res := rs.Apply(fileItem("/tmp/x", "text/plain", "anything"))
	if len(res.Errors) != 1 {
		t.Fatalf("want 1 error, got %d: %v", len(res.Errors), res.Errors)
	}
	if len(res.Tags) != 0 {
		t.Errorf("malformed rule should not contribute tags: %v", res.Tags)
	}
}

func TestTemplate_TitleAndNote(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{
			Name:  "email",
			Match: Match{Type: "email"},
			Actions: []Action{
				{SetTitle: "{{.Sender}} — {{.Subject}}"},
				{AppendNote: "matched: {{.Rule.Name}}"},
			},
		},
	}}
	body := "From: \"Alice\" <alice@example.com>\nSubject: Hi\n\nbody"
	res := rs.Apply(emailItem(body))
	if res.Title != `"Alice" <alice@example.com> — Hi` {
		t.Errorf("title=%q", res.Title)
	}
	if !strings.Contains(res.AppendedNote, "email") {
		t.Errorf("note=%q", res.AppendedNote)
	}
}

func TestLoad_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	rs, err := Load(filepath.Join(dir, "rules.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 0 {
		t.Errorf("expected empty ruleset, got %d rules", len(rs.Rules))
	}
}

func TestLoad_ParsesNewSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	src := `rules:
  - name: yt
    match:
      domain: youtube.com
    actions:
      - add_tags: [video, watch-later]
      - notify: "Video: {{.Title}}"
  - name: drop-noise
    match:
      domain: spam.example
    actions:
      - skip: true
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	rs, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 2 {
		t.Fatalf("want 2 rules, got %d", len(rs.Rules))
	}
	if len(rs.Rules[0].Actions) != 2 {
		t.Fatalf("yt rule: want 2 actions, got %d", len(rs.Rules[0].Actions))
	}
	if !contains(rs.Rules[0].Actions[0].AddTags, "video") {
		t.Errorf("yt action[0] missing video tag: %+v", rs.Rules[0].Actions[0])
	}
	if !rs.Rules[1].Actions[0].Skip {
		t.Errorf("drop-noise rule should have skip:true")
	}
}

func TestMigrate_LegacyAutotagYaml(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "autotag.yaml")
	newPath := filepath.Join(dir, "rules.yaml")

	legacySrc := `# top comment
rules:
  - name: yt
    match:
      domain: youtube.com
    add_tags: [video, watch-later]
  - name: invoices
    match:
      mime_type: application/pdf
    add_tags: [invoice, finance]
    add_collection: bills
`
	if err := os.WriteFile(legacy, []byte(legacySrc), 0o644); err != nil {
		t.Fatal(err)
	}

	rs, err := Load(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Rules) != 2 {
		t.Fatalf("want 2 rules after migration, got %d", len(rs.Rules))
	}

	// New file should exist
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("rules.yaml should exist after migration: %v", err)
	}
	// Legacy file removed
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy autotag.yaml should be gone: %v", err)
	}

	// Comment preserved
	migrated, _ := os.ReadFile(newPath)
	if !strings.Contains(string(migrated), "# top comment") {
		t.Errorf("comment lost in migration:\n%s", string(migrated))
	}

	// Apply produces the same effects
	res := rs.Apply(urlItem("https://youtube.com/watch?v=1"))
	if !contains(res.Tags, "video") {
		t.Errorf("yt rule lost effect after migration: tags=%v", res.Tags)
	}
	res = rs.Apply(fileItem("/tmp/x.pdf", "application/pdf", ""))
	if !contains(res.Tags, "invoice") || res.Collection != "bills" {
		t.Errorf("invoice rule lost effect: tags=%v collection=%q", res.Tags, res.Collection)
	}
}

func TestNamedRegexCaptures_ContentRegex(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{
			Name:  "invoice-amount",
			Match: Match{ContentRegex: `Amount:\s*(?P<amount>\$[0-9.]+)`},
			Actions: []Action{
				{SetTitle: "Invoice {{.Captures.amount}}"},
				{AppendNote: "Total: {{.Captures.amount}}"},
			},
		},
	}}
	item := &model.Item{
		Type:          model.TypeFile,
		MimeType:      "application/pdf",
		ExtractedText: "Customer: Acme\nAmount: $42.00\nDue: 2026-06-01",
	}
	res := rs.Apply(item)
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if res.Title != "Invoice $42.00" {
		t.Errorf("title=%q, want 'Invoice $42.00'", res.Title)
	}
	if !strings.Contains(res.AppendedNote, "$42.00") {
		t.Errorf("note=%q, want capture in note", res.AppendedNote)
	}
}

func TestNamedRegexCaptures_URLRegex(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{
			Name:  "yt-id",
			Match: Match{URLRegex: `/watch\?v=(?P<vid>[a-zA-Z0-9_-]+)`},
			Actions: []Action{
				{SetTitle: "YouTube: {{.Captures.vid}}"},
			},
		},
	}}
	res := rs.Apply(urlItem("https://youtube.com/watch?v=dQw4w9WgXcQ"))
	if res.Title != "YouTube: dQw4w9WgXcQ" {
		t.Errorf("title=%q", res.Title)
	}
}

func TestNamedRegexCaptures_MissingNameRendersEmpty(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{
			Name:  "weird",
			Match: Match{Type: "url"},
			Actions: []Action{
				{SetTitle: "Got: {{.Captures.nonexistent}}"},
			},
		},
	}}
	res := rs.Apply(urlItem("https://example.com"))
	// missingkey=zero option makes the missing key render as the empty
	// string instead of erroring.
	if res.Title != "Got:" {
		t.Errorf("title=%q, want 'Got:'", res.Title)
	}
	if len(res.Errors) != 0 {
		t.Errorf("expected no errors, got %v", res.Errors)
	}
}

func TestNamedRegexCaptures_BothRegexes(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{
			Name: "two-captures",
			Match: Match{
				URLRegex:     `/(?P<section>news|blog)/`,
				ContentRegex: `\bauthor:\s*(?P<author>\w+)`,
			},
			Actions: []Action{
				{SetTitle: "{{.Captures.section}} by {{.Captures.author}}"},
			},
		},
	}}
	item := &model.Item{
		Type:          model.TypeURL,
		URL:           "https://example.com/blog/post1",
		ExtractedText: "author: alice\nbody...",
	}
	res := rs.Apply(item)
	if res.Title != "blog by alice" {
		t.Errorf("title=%q, want 'blog by alice'", res.Title)
	}
}

func TestSenderHelpers(t *testing.T) {
	cases := []struct {
		from         string
		wantName     string
		wantEmail    string
		wantDomain   string
	}{
		{`"Alice Doe" <alice@example.com>`, "Alice Doe", "alice@example.com", "example.com"},
		{`Alice <alice@example.com>`, "Alice", "alice@example.com", "example.com"},
		{`alice@EXAMPLE.com`, "", "alice@EXAMPLE.com", "example.com"},
		{``, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.from, func(t *testing.T) {
			if got := senderName(tc.from); got != tc.wantName {
				t.Errorf("senderName(%q) = %q, want %q", tc.from, got, tc.wantName)
			}
			if got := senderEmail(tc.from); got != tc.wantEmail {
				t.Errorf("senderEmail(%q) = %q, want %q", tc.from, got, tc.wantEmail)
			}
			if got := senderDomain(tc.from); got != tc.wantDomain {
				t.Errorf("senderDomain(%q) = %q, want %q", tc.from, got, tc.wantDomain)
			}
		})
	}
}

func ptrTrue() *bool  { v := true; return &v }
func ptrFalse() *bool { v := false; return &v }

func TestMatch_IsDuplicateTrue(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{
			Name: "skip-dup-urls",
			Match: Match{
				Type:        "url",
				IsDuplicate: ptrTrue(),
			},
			Actions: []Action{{Skip: true}},
		},
	}}
	item := urlItem("https://example.com")

	// Without dup context — rule does NOT match.
	res := rs.Apply(item)
	if res.Skipped {
		t.Errorf("non-dup capture should not skip; skipped=%v", res.Skipped)
	}

	// With dup context — rule matches and skips.
	res = rs.ApplyWithContext(item, Context{IsDuplicate: true, DuplicateOf: "01ABC"})
	if !res.Skipped {
		t.Errorf("dup capture should skip; matched=%v", res.MatchedRules)
	}
	if res.SkippedBy != "skip-dup-urls" {
		t.Errorf("SkippedBy=%q, want skip-dup-urls", res.SkippedBy)
	}
}

func TestMatch_IsDuplicateFalse(t *testing.T) {
	// Tag fresh URLs only (excludes dups).
	rs := &Ruleset{Rules: []Rule{
		{
			Name: "tag-fresh",
			Match: Match{
				Type:        "url",
				IsDuplicate: ptrFalse(),
			},
			Actions: []Action{tagsAction("fresh")},
		},
	}}
	item := urlItem("https://example.com")

	res := rs.ApplyWithContext(item, Context{})
	if !contains(res.Tags, "fresh") {
		t.Errorf("non-dup capture should be tagged; tags=%v", res.Tags)
	}

	res = rs.ApplyWithContext(item, Context{IsDuplicate: true, DuplicateOf: "01ABC"})
	if contains(res.Tags, "fresh") {
		t.Errorf("dup capture should NOT be tagged; tags=%v", res.Tags)
	}
}

func TestDuplicateOfTemplate(t *testing.T) {
	rs := &Ruleset{Rules: []Rule{
		{
			Name: "link-and-tag-dup",
			Match: Match{
				Type:        "url",
				IsDuplicate: ptrTrue(),
			},
			Actions: []Action{
				{LinkTo: &LinkSpec{ID: "{{.DuplicateOf}}"}},
				tagsAction("dup-of-{{.DuplicateOfShort}}"),
			},
		},
	}}
	res := rs.ApplyWithContext(
		urlItem("https://example.com"),
		Context{IsDuplicate: true, DuplicateOf: "01ABCDEF12345"},
	)
	if len(res.Links) != 1 || res.Links[0].ID != "01ABCDEF12345" {
		t.Errorf("link_to.id should render template; got %+v", res.Links)
	}
	if !contains(res.Tags, "dup-of-01ABCDEF") {
		t.Errorf("add_tags should render and shorten; got %v", res.Tags)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
