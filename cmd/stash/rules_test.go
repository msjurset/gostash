package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/msjurset/gostash/internal/rules"

	"gopkg.in/yaml.v3"
)

func TestMatchSummary(t *testing.T) {
	tests := []struct {
		name string
		m    rules.Match
		want []string
	}{
		{name: "empty", m: rules.Match{}, want: []string{""}},
		{name: "single", m: rules.Match{Domain: "youtube.com"}, want: []string{"domain=youtube.com"}},
		{name: "multiple", m: rules.Match{Type: "url", Domain: "youtube.com", URLRegex: "/watch"},
			want: []string{"type=url", "domain=youtube.com", "url_regex=/watch"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchSummary(tc.m)
			for _, sub := range tc.want {
				if sub == "" {
					if got != "" {
						t.Errorf("expected empty, got %q", got)
					}
					continue
				}
				if !strings.Contains(got, sub) {
					t.Errorf("missing %q in %q", sub, got)
				}
			}
		})
	}
}

func TestActionSummary(t *testing.T) {
	tests := []struct {
		name string
		a    rules.Action
		want []string
	}{
		{name: "tags", a: rules.Action{AddTags: []string{"a", "b"}}, want: []string{"tags: a, b"}},
		{name: "skip", a: rules.Action{Skip: true}, want: []string{"SKIP"}},
		{name: "title", a: rules.Action{SetTitle: "X"}, want: []string{"title=X"}},
		{name: "link-tag", a: rules.Action{LinkTo: &rules.LinkSpec{Tag: "foo"}}, want: []string{"link_to.tag=foo"}},
		{name: "noop", a: rules.Action{}, want: []string{"(no-op)"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := actionSummary(tc.a)
			for _, sub := range tc.want {
				if !strings.Contains(got, sub) {
					t.Errorf("missing %q in %q", sub, got)
				}
			}
		})
	}
}

func parseDoc(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &doc
}

func TestFindRuleNode(t *testing.T) {
	src := `rules:
  - name: a
    match: {type: url}
    actions:
      - add_tags: [t1]
  - name: b
    match: {type: file}
    actions:
      - add_tags: [t2]
`
	doc := parseDoc(t, src)
	ruleA, err := findRuleNode(doc, "a")
	if err != nil {
		t.Fatal(err)
	}
	if ruleA.Kind != yaml.MappingNode {
		t.Errorf("expected mapping node, got kind=%v", ruleA.Kind)
	}
	if name := mappingValue(ruleA, "name"); name == nil || name.Value != "a" {
		t.Errorf("found wrong rule")
	}
	if _, err := findRuleNode(doc, "missing"); err == nil {
		t.Error("expected error for missing rule")
	}
}

func TestSetMappingBool_UpdateAndAppend(t *testing.T) {
	src := `rules:
  - name: a
    enabled: true
    match: {type: url}
    actions:
      - add_tags: [x]
`
	doc := parseDoc(t, src)
	rule, err := findRuleNode(doc, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := setMappingBool(rule, "enabled", false); err != nil {
		t.Fatal(err)
	}
	out, _ := yaml.Marshal(doc)
	if !strings.Contains(string(out), "enabled: false") {
		t.Errorf("expected enabled:false:\n%s", string(out))
	}

	// Append path: rule b has no enabled key.
	src2 := `rules:
  - name: b
    match: {type: url}
    actions:
      - add_tags: [x]
`
	doc2 := parseDoc(t, src2)
	rule, _ = findRuleNode(doc2, "b")
	if err := setMappingBool(rule, "enabled", false); err != nil {
		t.Fatal(err)
	}
	out, _ = yaml.Marshal(doc2)
	if !strings.Contains(string(out), "enabled: false") {
		t.Errorf("expected enabled:false appended:\n%s", string(out))
	}
}

func TestUpsertRule_AppendsThenReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(path, []byte("# header\nrules:\n  - name: a\n    match: {type: url}\n    actions:\n      - add_tags: [t1]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rule := rules.Rule{
		Name:    "b",
		Match:   rules.Match{Domain: "youtube.com"},
		Actions: []rules.Action{{AddTags: []string{"video"}}},
	}
	if err := upsertRule(path, rule); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	str := string(got)
	if !strings.Contains(str, "name: a") || !strings.Contains(str, "name: b") {
		t.Errorf("append failed:\n%s", str)
	}
	if !strings.Contains(str, "# header") {
		t.Errorf("header comment lost:\n%s", str)
	}

	// Now replace
	rule = rules.Rule{
		Name:    "b",
		Match:   rules.Match{Domain: "vimeo.com"},
		Actions: []rules.Action{{AddTags: []string{"video", "updated"}}},
	}
	if err := upsertRule(path, rule); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	str = string(got)
	if !strings.Contains(str, "vimeo.com") {
		t.Errorf("replace failed:\n%s", str)
	}
	if strings.Contains(str, "youtube.com") {
		t.Errorf("old domain still present:\n%s", str)
	}
	if c := strings.Count(str, "name: b"); c != 1 {
		t.Errorf("expected exactly one rule named 'b', got %d:\n%s", c, str)
	}
}

func TestUpsertRule_CreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "rules.yaml")
	rule := rules.Rule{
		Name:    "a",
		Match:   rules.Match{Type: "url"},
		Actions: []rules.Action{{AddTags: []string{"t1"}}},
	}
	if err := upsertRule(path, rule); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should have been created: %v", err)
	}
}

func TestRemoveRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	src := "# top\nrules:\n  - name: a\n    match: {type: url}\n    actions:\n      - add_tags: [t1]\n  - name: b\n    match: {type: file}\n    actions:\n      - add_tags: [t2]\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeRule(path, "a"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	str := string(got)
	if strings.Contains(str, "name: a") {
		t.Errorf("rule a should be gone:\n%s", str)
	}
	if !strings.Contains(str, "name: b") {
		t.Errorf("rule b lost:\n%s", str)
	}
	if !strings.Contains(str, "# top") {
		t.Errorf("comment lost:\n%s", str)
	}
	if err := removeRule(path, "missing"); err == nil {
		t.Error("expected error for missing rule")
	}
}
