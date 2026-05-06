package rules

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// migrateLegacy converts the previous autotag.yaml shape (top-level
// add_tags / add_collection per rule) into the new actions: list shape
// and writes it to `newPath`. The legacy file is removed after a
// successful migration so subsequent loads use the new file directly.
//
// Comments at the top level survive — we round-trip through yaml.Node and
// rebuild the rule mappings.
func migrateLegacy(legacyPath, newPath string) error {
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", legacyPath, err)
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("legacy file %s is empty", legacyPath)
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("legacy file %s root is not a mapping", legacyPath)
	}

	rulesSeq := mappingValueLegacy(root, "rules")
	if rulesSeq == nil || rulesSeq.Kind != yaml.SequenceNode {
		return fmt.Errorf("legacy file %s has no `rules:` sequence", legacyPath)
	}

	for _, ruleNode := range rulesSeq.Content {
		if ruleNode.Kind != yaml.MappingNode {
			continue
		}
		var actionEntries []*yaml.Node
		var keepIndices []int
		for i := 0; i+1 < len(ruleNode.Content); i += 2 {
			key := ruleNode.Content[i].Value
			val := ruleNode.Content[i+1]
			switch key {
			case "add_tags":
				actionEntries = append(actionEntries, makeActionMap("add_tags", val))
			case "add_collection":
				actionEntries = append(actionEntries, makeActionMap("add_collection", val))
			default:
				keepIndices = append(keepIndices, i)
			}
		}

		if len(actionEntries) == 0 {
			continue
		}

		// Rebuild the rule mapping: keep all non-action keys, then append
		// `actions:` with a sequence of single-key maps.
		newContent := make([]*yaml.Node, 0, len(keepIndices)*2+2)
		for _, idx := range keepIndices {
			newContent = append(newContent, ruleNode.Content[idx], ruleNode.Content[idx+1])
		}
		actionsKey := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "actions"}
		actionsSeq := &yaml.Node{Kind: yaml.SequenceNode, Content: actionEntries}
		newContent = append(newContent, actionsKey, actionsSeq)
		ruleNode.Content = newContent
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal migrated rules: %w", err)
	}
	if err := os.WriteFile(newPath, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", newPath, err)
	}
	if err := os.Remove(legacyPath); err != nil {
		// Migration succeeded but cleanup failed — log to stderr but don't
		// fail the load. Worst case the user has both files; the new one
		// wins on next load.
		fmt.Fprintf(os.Stderr, "warning: migrated %s → %s but couldn't remove the old file: %v\n",
			legacyPath, newPath, err)
	}
	return nil
}

func makeActionMap(key string, value *yaml.Node) *yaml.Node {
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	return &yaml.Node{
		Kind:    yaml.MappingNode,
		Content: []*yaml.Node{keyNode, value},
	}
}

// mappingValueLegacy is the migration-internal copy of the helper used by
// the rules CLI subcommands, kept here to avoid coupling internal/rules to
// cmd/stash.
func mappingValueLegacy(m *yaml.Node, key string) *yaml.Node {
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
