package main

import (
	"fmt"
	"strings"

	"github.com/msjurset/gostash/internal/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Read and edit Stash configuration",
}

// stash config exclusions — manage the URL-redact rules that
// rewrite a captured item's URL field at capture time (without
// blocking the capture itself). Used for transient session URLs
// like Gemini chat threads where the URL is never re-visitable.
var configExclusionsCmd = &cobra.Command{
	Use:   "exclusions",
	Short: "Manage URL-redact rules",
}

var configExclusionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured URL exclusions",
	RunE:  runConfigExclusionsList,
}

var configExclusionsAddCmd = &cobra.Command{
	Use:   "add <pattern>",
	Short: "Add a URL exclusion rule",
	Long: "Add a rule that rewrites the URL field of any matching\n" +
		"captured item. The capture itself still happens — only the URL\n" +
		"column is redacted.\n\n" +
		"  pattern    Domain (default) or RE2 regex. For \"domain\" match\n" +
		"             a literal hostname (e.g. \"gemini.google.com\") or a\n" +
		"             \"*.suffix\" wildcard (\"*.googleusercontent.com\").\n\n" +
		"Flags:\n" +
		"  --match    \"domain\" (default) or \"regex\".\n" +
		"  --behavior \"domain\" (default — keep scheme + host, drop path)\n" +
		"             or \"clear\" (drop URL entirely).",
	Args: cobra.ExactArgs(1),
	RunE: runConfigExclusionsAdd,
}

var configExclusionsRemoveCmd = &cobra.Command{
	Use:   "remove <pattern>",
	Short: "Remove a URL exclusion rule by exact pattern match",
	Args:  cobra.ExactArgs(1),
	RunE:  runConfigExclusionsRemove,
}

func init() {
	configExclusionsAddCmd.Flags().String("match", "domain", "Match type: domain | regex")
	configExclusionsAddCmd.Flags().String("behavior", "domain", "Behavior on match: domain | clear")

	configExclusionsCmd.AddCommand(configExclusionsListCmd)
	configExclusionsCmd.AddCommand(configExclusionsAddCmd)
	configExclusionsCmd.AddCommand(configExclusionsRemoveCmd)
	configCmd.AddCommand(configExclusionsCmd)
	rootCmd.AddCommand(configCmd)
}

func runConfigExclusionsList(cmd *cobra.Command, args []string) error {
	c := config.Get()
	if flagJSON {
		// Wrap in an object so the shape mirrors other list-style
		// outputs and leaves room for future fields.
		printJSON(map[string]any{
			"exclusions": c.Exclusions,
		})
		return nil
	}
	if len(c.Exclusions) == 0 {
		fmt.Println("(no exclusions configured)")
		return nil
	}
	for _, ex := range c.Exclusions {
		match := ex.Match
		if match == "" {
			match = "domain"
		}
		behavior := ex.Behavior
		if behavior == "" {
			behavior = "domain"
		}
		fmt.Printf("  %-40s  match=%s  behavior=%s\n", ex.Pattern, match, behavior)
	}
	return nil
}

func runConfigExclusionsAdd(cmd *cobra.Command, args []string) error {
	pattern := strings.TrimSpace(args[0])
	if pattern == "" {
		return fmt.Errorf("pattern is required")
	}
	match, _ := cmd.Flags().GetString("match")
	behavior, _ := cmd.Flags().GetString("behavior")
	match = strings.ToLower(strings.TrimSpace(match))
	behavior = strings.ToLower(strings.TrimSpace(behavior))
	switch match {
	case "", "domain", "regex":
	default:
		return fmt.Errorf("invalid --match %q (expected domain | regex)", match)
	}
	switch behavior {
	case "", "domain", "clear":
	default:
		return fmt.Errorf("invalid --behavior %q (expected domain | clear)", behavior)
	}

	c := config.Get()
	// Replace an existing rule with the same pattern so re-adding
	// is idempotent and acts as a single-row edit from the Mac UI.
	replaced := false
	for i, ex := range c.Exclusions {
		if ex.Pattern == pattern {
			c.Exclusions[i].Match = match
			c.Exclusions[i].Behavior = behavior
			replaced = true
			break
		}
	}
	if !replaced {
		c.Exclusions = append(c.Exclusions, config.Exclusion{
			Pattern:  pattern,
			Match:    match,
			Behavior: behavior,
		})
	}

	if err := config.Save(c); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	config.Reload()
	if flagJSON {
		printJSON(map[string]any{
			"ok":         true,
			"replaced":   replaced,
			"exclusions": c.Exclusions,
		})
	} else if replaced {
		fmt.Printf("Updated exclusion %q\n", pattern)
	} else {
		fmt.Printf("Added exclusion %q\n", pattern)
	}
	return nil
}

func runConfigExclusionsRemove(cmd *cobra.Command, args []string) error {
	pattern := args[0]
	c := config.Get()
	out := make([]config.Exclusion, 0, len(c.Exclusions))
	removed := false
	for _, ex := range c.Exclusions {
		if ex.Pattern == pattern && !removed {
			removed = true
			continue
		}
		out = append(out, ex)
	}
	c.Exclusions = out
	if err := config.Save(c); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	config.Reload()
	if flagJSON {
		printJSON(map[string]any{
			"ok":         true,
			"removed":    removed,
			"exclusions": c.Exclusions,
		})
	} else if removed {
		fmt.Printf("Removed exclusion %q\n", pattern)
	} else {
		fmt.Printf("No exclusion with pattern %q\n", pattern)
	}
	return nil
}
