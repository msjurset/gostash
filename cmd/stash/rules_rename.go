package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/msjurset/gostash/internal/config"
	"github.com/msjurset/gostash/internal/rules"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var rulesRenameCmd = &cobra.Command{
	Use:   "rename <old> <new>",
	Short: "Rename a rule, updating rules.yaml and rules.log in place",
	Long: `Rename a rule. Updates the rule's name field in rules.yaml
(preserving comments via yaml.Node round-trip) and rewrites every entry
in rules.log so historical activity for the rule keeps showing up under
the new name.

Errors out if the new name is empty, equal to the old name, or already
in use by another rule.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		oldName := strings.TrimSpace(args[0])
		newName := strings.TrimSpace(args[1])
		if oldName == "" {
			return fmt.Errorf("old name is required")
		}
		if newName == "" {
			return fmt.Errorf("new name is required")
		}
		if oldName == newName {
			return fmt.Errorf("new name is the same as the old name")
		}

		dir := config.Dir()
		rulesPath := rules.DefaultPath(dir)
		if err := renameRuleInYAML(rulesPath, oldName, newName); err != nil {
			return err
		}

		logPath := rules.DefaultLogPath(dir)
		updated, err := renameRuleInLog(logPath, oldName, newName)
		if err != nil {
			// Log rewrite is best-effort — the YAML rename already
			// succeeded, so don't fail the whole operation.
			fmt.Fprintf(os.Stderr, "warning: rewrite %s: %v\n", logPath, err)
		}

		if flagJSON {
			printJSON(map[string]any{
				"old":            oldName,
				"new":            newName,
				"events_updated": updated,
			})
			return nil
		}
		fmt.Printf("Renamed rule %q → %q (updated %d log entries)\n", oldName, newName, updated)
		return nil
	},
}

// renameRuleInYAML updates the `name` scalar of the rule named `oldName`
// in `path` to `newName`, preserving comments and ordering. Errors if
// the old name is missing or the new name already exists.
func renameRuleInYAML(path, oldName, newName string) error {
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

	var oldNode *yaml.Node
	for _, node := range rulesSeq.Content {
		if node.Kind != yaml.MappingNode {
			continue
		}
		nameNode := mappingValue(node, "name")
		if nameNode == nil {
			continue
		}
		if nameNode.Value == newName {
			return fmt.Errorf("a rule named %q already exists", newName)
		}
		if nameNode.Value == oldName {
			oldNode = nameNode
		}
	}
	if oldNode == nil {
		return fmt.Errorf("rule %q not found", oldName)
	}
	oldNode.Value = newName
	return writeDoc(path, &doc)
}

// renameRuleInLog rewrites every entry in `path` whose `rules` array
// contains `oldName`, replacing it with `newName`. Returns the number of
// entries updated. Missing log file is not an error (no history to rewrite).
func renameRuleInLog(path, oldName, newName string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	tmp, err := os.CreateTemp(filepath.Dir(path), ".rules.log.rename.*")
	if err != nil {
		return 0, fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if we don't get to the rename.
	defer os.Remove(tmpPath)

	scanner := bufio.NewScanner(f)
	// rules.log lines can be larger than the default 64KB token limit
	// when an event includes a long source URL or note.
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	writer := bufio.NewWriter(tmp)

	updated := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			writer.Write([]byte("\n"))
			continue
		}
		var ev rules.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			// Malformed line — preserve verbatim so we don't lose data.
			writer.Write(line)
			writer.WriteByte('\n')
			continue
		}
		changed := false
		for i, r := range ev.Rules {
			if r == oldName {
				ev.Rules[i] = newName
				changed = true
			}
		}
		if changed {
			updated++
			b, err := json.Marshal(ev)
			if err != nil {
				return updated, fmt.Errorf("encode event: %w", err)
			}
			writer.Write(b)
			writer.WriteByte('\n')
		} else {
			writer.Write(line)
			writer.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return updated, fmt.Errorf("scan %s: %w", path, err)
	}
	if err := writer.Flush(); err != nil {
		return updated, fmt.Errorf("flush temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return updated, fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return updated, fmt.Errorf("rename temp: %w", err)
	}
	return updated, nil
}

func init() {
	rulesCmd.AddCommand(rulesRenameCmd)
}
