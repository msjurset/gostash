package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/model"
	"github.com/msjurset/gostash/internal/rules"
	"github.com/msjurset/gostash/internal/store"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// rulesCmd is the parent for `stash rules …` subcommands. The rules file
// itself lives at $STASH_DIR/rules.yaml; these subcommands inspect it,
// apply rules to existing items, and toggle individual rules without
// requiring a hand-edit.
var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Inspect and apply capture rules",
	Long: `Capture rules in $STASH_DIR/rules.yaml are applied to every new
item ingested via 'stash add'. Each rule has match conditions plus a list
of actions: add tags, assign a collection, override the title, append
notes, fire notifications, link to other items, or skip the item entirely.

These subcommands let you review which rules exist, preview what would
happen for a specific item, retroactively run rules over already-stashed
items, and toggle rules without hand-editing the YAML.`,
}

var rulesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		rs, err := loadRules()
		if err != nil {
			return err
		}
		if flagJSON {
			printJSONSlice(rs.Rules)
			return nil
		}
		if len(rs.Rules) == 0 {
			fmt.Println("No rules configured.")
			fmt.Printf("Create %s to define rules.\n", rules.DefaultPath(config.Dir()))
			return nil
		}
		for _, r := range rs.Rules {
			state := "enabled"
			if !r.IsEnabled() {
				state = "DISABLED"
			}
			fmt.Printf("- %s [%s]\n", r.Name, state)
			if r.Description != "" {
				fmt.Printf("    %s\n", r.Description)
			}
			if summary := matchSummary(r.Match); summary != "" {
				fmt.Printf("    match: %s\n", summary)
			}
			for _, action := range r.Actions {
				fmt.Printf("    %s\n", actionSummary(action))
			}
		}
		return nil
	},
}

var rulesTestCmd = &cobra.Command{
	Use:   "test <id>",
	Short: "Show which rules would apply to an existing item (no writes)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		item, err := s.GetItem(context.Background(), args[0])
		if err != nil {
			return err
		}

		rs, err := loadRules()
		if err != nil {
			return err
		}

		// Strip existing tags before evaluating so we see what each rule
		// WOULD add. Apply otherwise dedupes against current tags and
		// hides the fact that a rule matched if its tags are already on
		// the item. For `test`, the user wants to see all matches.
		preview := *item
		preview.Tags = nil
		result := rs.Apply(&preview)

		out := struct {
			ItemID             string       `json:"item_id"`
			Title              string       `json:"title"`
			MatchedRules       []string     `json:"matched_rules"`
			WouldAddTags       []string     `json:"would_add_tags"`
			NewTags            []string     `json:"new_tags"`
			WouldAddCollection string       `json:"would_add_collection,omitempty"`
			WouldSetTitle      string       `json:"would_set_title,omitempty"`
			WouldSetNote       string       `json:"would_set_note,omitempty"`
			WouldAppendNote    string       `json:"would_append_note,omitempty"`
			WouldNotify        []string     `json:"would_notify,omitempty"`
			WouldLink          []rules.LinkSpec `json:"would_link,omitempty"`
			WouldSkip          bool         `json:"would_skip"`
			SkippedBy          string       `json:"skipped_by,omitempty"`
			CurrentTags        []string     `json:"current_tags"`
			CurrentCollections []string     `json:"current_collections"`
			Errors             []string     `json:"errors,omitempty"`
		}{
			ItemID:             item.ID,
			Title:              item.Title,
			MatchedRules:       result.MatchedRules,
			WouldAddTags:       result.Tags,
			WouldAddCollection: result.Collection,
			WouldSetTitle:      result.Title,
			WouldSetNote:       result.Note,
			WouldAppendNote:    result.AppendedNote,
			WouldNotify:        result.Notifies,
			WouldLink:          result.Links,
			WouldSkip:          result.Skipped,
			SkippedBy:          result.SkippedBy,
		}
		for _, t := range item.Tags {
			out.CurrentTags = append(out.CurrentTags, t.Name)
		}
		for _, c := range item.Collections {
			out.CurrentCollections = append(out.CurrentCollections, c.Name)
		}
		existing := map[string]struct{}{}
		for _, t := range out.CurrentTags {
			existing[strings.ToLower(t)] = struct{}{}
		}
		for _, t := range result.Tags {
			if _, dup := existing[strings.ToLower(t)]; !dup {
				out.NewTags = append(out.NewTags, t)
			}
		}
		for _, e := range result.Errors {
			out.Errors = append(out.Errors, e.Error())
		}

		if flagJSON {
			printJSON(out)
			return nil
		}

		fmt.Printf("Item: %s [%s]\n", item.Title, item.ID)
		if len(out.MatchedRules) == 0 {
			fmt.Println("No rules match.")
		} else {
			fmt.Printf("Matched: %s\n", strings.Join(out.MatchedRules, ", "))
			if out.WouldSkip {
				fmt.Printf("⚠️  Would SKIP (rule: %s) — item would not be saved.\n", out.SkippedBy)
			}
			if len(out.NewTags) > 0 {
				fmt.Printf("Add tags: %s\n", strings.Join(out.NewTags, ", "))
			}
			if out.WouldAddCollection != "" {
				if len(out.CurrentCollections) > 0 {
					fmt.Printf("Collection (suppressed — already in %s): %s\n",
						strings.Join(out.CurrentCollections, ", "), out.WouldAddCollection)
				} else {
					fmt.Printf("Add to collection: %s\n", out.WouldAddCollection)
				}
			}
			if out.WouldSetTitle != "" {
				fmt.Printf("Set title: %s\n", out.WouldSetTitle)
			}
			if out.WouldSetNote != "" {
				fmt.Printf("Set note: %s\n", out.WouldSetNote)
			}
			if out.WouldAppendNote != "" {
				fmt.Printf("Append note: %s\n", out.WouldAppendNote)
			}
			for _, n := range out.WouldNotify {
				fmt.Printf("Notify: %s\n", n)
			}
			for _, l := range out.WouldLink {
				if l.Tag != "" {
					fmt.Printf("Link to all items with tag: %s\n", l.Tag)
				}
				if l.ID != "" {
					fmt.Printf("Link to item: %s\n", l.ID)
				}
			}
		}
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "warning: %v\n", e)
		}
		return nil
	},
}

var rulesApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Retroactively apply rules to existing items",
	Long: `Run all enabled rules over existing items and apply their actions:
add tags, assign collections, set/append notes, link to other items.

Filter to a subset of items with --type, --tag, or --rule. Use --dry-run
to preview changes without writing anything.

Skip and notify actions DO NOT apply to retroactive runs — they only fire
on capture (skip would mean deleting an existing item, and notifications
about already-saved items are noise).

Items that already have a rule's tag are unchanged for that tag (safe to
re-run). Items that already have ANY collection are not assigned a new
collection, mirroring the 'stash add' precedence rule.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		filterType, _ := cmd.Flags().GetString("type")
		filterTags, _ := cmd.Flags().GetStringSlice("tag")
		onlyRule, _ := cmd.Flags().GetString("rule")

		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		rs, err := loadRules()
		if err != nil {
			return err
		}
		if onlyRule != "" {
			filtered := rules.Ruleset{}
			for _, r := range rs.Rules {
				if r.Name == onlyRule {
					filtered.Rules = append(filtered.Rules, r)
					break
				}
			}
			if len(filtered.Rules) == 0 {
				return fmt.Errorf("rule %q not found", onlyRule)
			}
			rs = &filtered
		}

		filter := model.ItemFilter{Tags: filterTags, Limit: 100000}
		if filterType != "" {
			filter.Type = model.ParseItemType(filterType)
		}
		ctx := context.Background()
		items, err := s.ListItems(ctx, filter)
		if err != nil {
			return err
		}

		summary := applySummary(items, rs, s, ctx, dryRun)

		if flagJSON {
			printJSON(summary)
			return nil
		}

		fmt.Printf("Evaluated: %d items\n", summary.Evaluated)
		fmt.Printf("Changed:   %d items\n", summary.Changed)
		fmt.Printf("Tags added:        %d\n", summary.TagsAdded)
		fmt.Printf("Collections added: %d\n", summary.CollectionsAdded)
		fmt.Printf("Titles set:        %d\n", summary.TitlesSet)
		fmt.Printf("Notes updated:     %d\n", summary.NotesUpdated)
		if dryRun {
			fmt.Println("(dry run — no writes performed)")
		}
		return nil
	},
}

var rulesSaveCmd = &cobra.Command{
	Use:   "save",
	Short: "Upsert a rule from a JSON document on stdin",
	Long: `Read a single rule as JSON from stdin and write it to rules.yaml,
preserving comments and other rules. If a rule with the same name already
exists it is replaced; otherwise the new rule is appended.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var rule rules.Rule
		dec := json.NewDecoder(os.Stdin)
		if err := dec.Decode(&rule); err != nil {
			return fmt.Errorf("decode rule from stdin: %w", err)
		}
		if strings.TrimSpace(rule.Name) == "" {
			return fmt.Errorf("rule name is required")
		}
		path := rules.DefaultPath(config.Dir())
		if err := upsertRule(path, rule); err != nil {
			return err
		}
		if flagJSON {
			printJSON(rule)
			return nil
		}
		fmt.Printf("Saved rule %q\n", rule.Name)
		return nil
	},
}

var rulesRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Delete a rule by name",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := rules.DefaultPath(config.Dir())
		if err := removeRule(path, args[0]); err != nil {
			return err
		}
		if flagJSON {
			printJSON(map[string]any{"removed": args[0]})
			return nil
		}
		fmt.Printf("Removed rule %q\n", args[0])
		return nil
	},
}

var rulesEnableCmd = &cobra.Command{
	Use:   "enable <name>",
	Short: "Enable a rule by name",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setRuleEnabled(args[0], true)
	},
}

var rulesDisableCmd = &cobra.Command{
	Use:   "disable <name>",
	Short: "Disable a rule by name (rule remains in file but won't fire)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setRuleEnabled(args[0], false)
	},
}

func init() {
	rulesApplyCmd.Flags().Bool("dry-run", false, "Preview changes without writing")
	rulesApplyCmd.Flags().String("type", "", "Limit to one item type (url, snippet, file, image, email)")
	rulesApplyCmd.Flags().StringSlice("tag", nil, "Limit to items with these tags (repeatable)")
	rulesApplyCmd.Flags().String("rule", "", "Apply only the named rule")

	rulesCmd.AddCommand(rulesListCmd)
	rulesCmd.AddCommand(rulesTestCmd)
	rulesCmd.AddCommand(rulesApplyCmd)
	rulesCmd.AddCommand(rulesSaveCmd)
	rulesCmd.AddCommand(rulesRemoveCmd)
	rulesCmd.AddCommand(rulesEnableCmd)
	rulesCmd.AddCommand(rulesDisableCmd)
	rootCmd.AddCommand(rulesCmd)
}

// loadRules reads the configured rules file. Returns an empty ruleset (no
// error) if the file doesn't exist. Silently migrates legacy autotag.yaml
// on first load.
func loadRules() (*rules.Ruleset, error) {
	return rules.Load(rules.DefaultPath(config.Dir()))
}

// matchSummary renders a Match block as a one-line, human-readable summary
// for `rules list` output.
func matchSummary(m rules.Match) string {
	var parts []string
	if m.Type != "" {
		parts = append(parts, "type="+m.Type)
	}
	if m.Domain != "" {
		parts = append(parts, "domain="+m.Domain)
	}
	if m.URLRegex != "" {
		parts = append(parts, "url_regex="+m.URLRegex)
	}
	if m.MimeType != "" {
		parts = append(parts, "mime="+m.MimeType)
	}
	if m.MimeTypePrefix != "" {
		parts = append(parts, "mime_prefix="+m.MimeTypePrefix)
	}
	if m.Sender != "" {
		parts = append(parts, "sender="+m.Sender)
	}
	if m.SenderDomain != "" {
		parts = append(parts, "sender_domain="+m.SenderDomain)
	}
	if m.PathGlob != "" {
		parts = append(parts, "path_glob="+m.PathGlob)
	}
	if m.Content != "" {
		parts = append(parts, "content~="+m.Content)
	}
	if m.ContentRegex != "" {
		parts = append(parts, "content_regex="+m.ContentRegex)
	}
	return strings.Join(parts, ", ")
}

// actionSummary renders a single Action as a one-line description for
// `rules list` output.
func actionSummary(a rules.Action) string {
	var parts []string
	if len(a.AddTags) > 0 {
		parts = append(parts, "tags: "+strings.Join(a.AddTags, ", "))
	}
	if a.AddCollection != "" {
		parts = append(parts, "collection: "+a.AddCollection)
	}
	if a.SetTitle != "" {
		parts = append(parts, "title="+a.SetTitle)
	}
	if a.SetNote != "" {
		parts = append(parts, "note="+a.SetNote)
	}
	if a.AppendNote != "" {
		parts = append(parts, "append_note="+a.AppendNote)
	}
	if a.Skip {
		parts = append(parts, "SKIP")
	}
	if a.Notify != "" {
		parts = append(parts, "notify="+a.Notify)
	}
	if a.LinkTo != nil {
		if a.LinkTo.Tag != "" {
			parts = append(parts, "link_to.tag="+a.LinkTo.Tag)
		}
		if a.LinkTo.ID != "" {
			parts = append(parts, "link_to.id="+a.LinkTo.ID)
		}
	}
	if len(parts) == 0 {
		return "(no-op)"
	}
	return strings.Join(parts, "; ")
}

// ApplySummary is the JSON-serializable result of `rules apply`.
type ApplySummary struct {
	Evaluated        int           `json:"evaluated"`
	Changed          int           `json:"changed"`
	TagsAdded        int           `json:"tags_added"`
	CollectionsAdded int           `json:"collections_added"`
	TitlesSet        int           `json:"titles_set"`
	NotesUpdated     int           `json:"notes_updated"`
	DryRun           bool          `json:"dry_run"`
	Changes          []ApplyChange `json:"changes,omitempty"`
}

// ApplyChange records one item's modifications. Empty AddedTags AND empty
// AddedCollection (and other fields) means the item was evaluated but
// unchanged; such entries are omitted from the summary.
type ApplyChange struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	AddedTags       []string `json:"added_tags,omitempty"`
	AddedCollection string   `json:"added_collection,omitempty"`
	NewTitle        string   `json:"new_title,omitempty"`
	NoteChanged     bool     `json:"note_changed,omitempty"`
}

func applySummary(items []model.Item, rs *rules.Ruleset, s store.Store, ctx context.Context, dryRun bool) ApplySummary {
	var sum ApplySummary
	sum.DryRun = dryRun
	sum.Evaluated = len(items)
	for i := range items {
		item := &items[i]
		full, err := s.GetItem(ctx, item.ID)
		if err != nil {
			continue
		}
		result := rs.Apply(full)

		// skip / notify don't fire on retroactive apply — they're
		// capture-time concerns.

		var change ApplyChange
		change.ID = full.ID
		change.Title = full.Title

		for _, tag := range result.Tags {
			if !dryRun {
				if err := s.AddTag(ctx, full.ID, tag); err != nil {
					fmt.Fprintf(os.Stderr, "warning: add tag %q to %s: %v\n", tag, full.ID, err)
					continue
				}
			}
			change.AddedTags = append(change.AddedTags, tag)
			sum.TagsAdded++
		}

		if result.Collection != "" && len(full.Collections) == 0 {
			if !dryRun {
				if _, err := s.GetCollection(ctx, result.Collection); err != nil {
					if _, err := s.CreateCollection(ctx, result.Collection, ""); err != nil {
						fmt.Fprintf(os.Stderr, "warning: create collection %q: %v\n", result.Collection, err)
					}
				}
				if err := s.AddToCollection(ctx, full.ID, result.Collection); err != nil {
					fmt.Fprintf(os.Stderr, "warning: assign collection %q to %s: %v\n", result.Collection, full.ID, err)
				}
			}
			change.AddedCollection = result.Collection
			sum.CollectionsAdded++
		}

		if result.Title != "" && full.Title != result.Title {
			full.Title = result.Title
			change.NewTitle = result.Title
			sum.TitlesSet++
			if !dryRun {
				if err := s.UpdateItem(ctx, full); err != nil {
					fmt.Fprintf(os.Stderr, "warning: update title on %s: %v\n", full.ID, err)
				}
			}
		}

		if result.HasNoteUpdate() {
			merged := result.MergedNote(full.Notes)
			if merged != full.Notes {
				full.Notes = merged
				change.NoteChanged = true
				sum.NotesUpdated++
				if !dryRun {
					if err := s.UpdateItem(ctx, full); err != nil {
						fmt.Fprintf(os.Stderr, "warning: update note on %s: %v\n", full.ID, err)
					}
				}
			}
		}

		if change.AddedTags != nil || change.AddedCollection != "" || change.NewTitle != "" || change.NoteChanged {
			sum.Changed++
			sum.Changes = append(sum.Changes, change)
			// Log the retro fire so the activity feed reflects the run.
			// `--dry-run` doesn't log because the change isn't real.
			if !dryRun {
				logRuleRetro(full, result)
			}
		}
	}
	return sum
}

// upsertRule writes the given rule to the rules file, replacing any
// existing rule with the same name or appending a new one. Preserves
// comments and other rules via yaml.Node round-trip. Creates the file
// (and parent directory) if it does not yet exist.
func upsertRule(path string, rule rules.Rule) error {
	doc, err := readOrCreateDoc(path)
	if err != nil {
		return err
	}
	rulesSeq, err := ensureRulesSequence(doc)
	if err != nil {
		return err
	}

	var newRule yaml.Node
	if err := newRule.Encode(rule); err != nil {
		return fmt.Errorf("encode rule: %w", err)
	}

	for i, node := range rulesSeq.Content {
		if node.Kind != yaml.MappingNode {
			continue
		}
		if name := mappingValue(node, "name"); name != nil && name.Value == rule.Name {
			rulesSeq.Content[i] = &newRule
			return writeDoc(path, doc)
		}
	}
	rulesSeq.Content = append(rulesSeq.Content, &newRule)
	return writeDoc(path, doc)
}

// removeRule deletes the named rule from the rules file.
func removeRule(path, name string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("rules file is empty")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("rules file root is not a mapping")
	}
	rulesSeq := mappingValue(root, "rules")
	if rulesSeq == nil || rulesSeq.Kind != yaml.SequenceNode {
		return fmt.Errorf("rules file has no `rules:` sequence")
	}
	for i, node := range rulesSeq.Content {
		if node.Kind != yaml.MappingNode {
			continue
		}
		if n := mappingValue(node, "name"); n != nil && n.Value == name {
			rulesSeq.Content = append(rulesSeq.Content[:i], rulesSeq.Content[i+1:]...)
			return writeDoc(path, &doc)
		}
	}
	return fmt.Errorf("rule %q not found", name)
}

func readOrCreateDoc(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		return &yaml.Node{
			Kind: yaml.DocumentNode,
			Content: []*yaml.Node{
				{
					Kind: yaml.MappingNode,
					Content: []*yaml.Node{
						{Kind: yaml.ScalarNode, Tag: "!!str", Value: "rules"},
						{Kind: yaml.SequenceNode},
					},
				},
			},
		}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &doc, nil
}

func ensureRulesSequence(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind != yaml.DocumentNode {
		return nil, fmt.Errorf("expected document node, got kind=%v", doc.Kind)
	}
	if len(doc.Content) == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode}
		doc.Content = []*yaml.Node{root}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("rules file root is not a mapping")
	}
	if seq := mappingValue(root, "rules"); seq != nil {
		if seq.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("`rules:` is not a sequence")
		}
		return seq, nil
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "rules"}
	valNode := &yaml.Node{Kind: yaml.SequenceNode}
	root.Content = append(root.Content, keyNode, valNode)
	return valNode, nil
}

func writeDoc(path string, doc *yaml.Node) error {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// setRuleEnabled toggles the `enabled:` flag on a named rule, preserving
// comments and ordering by round-tripping through yaml.Node.
func setRuleEnabled(name string, enabled bool) error {
	path := rules.DefaultPath(config.Dir())
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	rule, err := findRuleNode(&doc, name)
	if err != nil {
		return err
	}
	if err := setMappingBool(rule, "enabled", enabled); err != nil {
		return err
	}

	if err := writeDoc(path, &doc); err != nil {
		return err
	}

	if flagJSON {
		printJSON(map[string]any{"name": name, "enabled": enabled})
		return nil
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Printf("Rule %q %s.\n", name, state)
	return nil
}

func findRuleNode(doc *yaml.Node, name string) (*yaml.Node, error) {
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("rules file is empty")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("rules file root is not a mapping")
	}
	rulesSeq := mappingValue(root, "rules")
	if rulesSeq == nil || rulesSeq.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("rules file has no `rules:` sequence")
	}
	for _, ruleNode := range rulesSeq.Content {
		if ruleNode.Kind != yaml.MappingNode {
			continue
		}
		nameNode := mappingValue(ruleNode, "name")
		if nameNode != nil && nameNode.Value == name {
			return ruleNode, nil
		}
	}
	return nil, fmt.Errorf("rule %q not found", name)
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setMappingBool(m *yaml.Node, key string, value bool) error {
	if m.Kind != yaml.MappingNode {
		return fmt.Errorf("target is not a mapping")
	}
	valStr := "true"
	if !value {
		valStr = "false"
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1].Kind = yaml.ScalarNode
			m.Content[i+1].Tag = "!!bool"
			m.Content[i+1].Value = valStr
			m.Content[i+1].Style = 0
			return nil
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: valStr}
	m.Content = append(m.Content, keyNode, valNode)
	return nil
}
