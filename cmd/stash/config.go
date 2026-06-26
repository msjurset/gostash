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

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current configuration as JSON",
	RunE: func(cmd *cobra.Command, args []string) error {
		c := config.Get()
		type ConfigResponse struct {
			config.Config
			PaidCredentialSet bool `json:"paid_credential_set"`
		}
		printJSON(ConfigResponse{
			Config:            c,
			PaidCredentialSet: c.PaidCredential != "",
		})
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration parameter",
	Long: "Set a configuration parameter. Supported keys:\n" +
		"  primary_model                (string)\n" +
		"  ai_models                    (comma-separated model list)\n" +
		"  max_daily_budget_usd         (number)\n" +
		"  max_monthly_budget_usd       (number)\n" +
		"  paid_tier_enabled            (true/false)\n" +
		"  paid_credential              (string/op:// reference)\n" +
		"  paid_approval_duration_hours (number)\n" +
		"  max_video_duration_minutes   (number)\n\n" +
		"Per-operation overrides (operations.<op>.<field>):\n" +
		"  operations.<op>.primary_model\n" +
		"  operations.<op>.ai_models\n" +
		"  (where <op> is identify, search, chat, ask, transform)",
	Args: cobra.ExactArgs(2),
	RunE: runConfigSet,
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	key := strings.ToLower(strings.TrimSpace(args[0]))
	val := strings.TrimSpace(args[1])

	c := config.Get()
	if strings.HasPrefix(key, "operations.") {
		parts := strings.Split(key, ".")
		if len(parts) != 3 {
			return fmt.Errorf("invalid operations key: %q (expected operations.<op>.<field>)", key)
		}
		opName := parts[1]
		fieldName := parts[2]

		switch opName {
		case "identify", "search", "chat", "ask", "transform":
		default:
			return fmt.Errorf("unsupported operation name: %q (expected identify | search | chat | ask | transform)", opName)
		}

		if c.Operations == nil {
			c.Operations = make(map[string]config.OperationConfig)
		}
		opCfg := c.Operations[opName]

		switch fieldName {
		case "primary_model":
			opCfg.PrimaryModel = val
		case "ai_models":
			var models []string
			for _, m := range strings.Split(val, ",") {
				m = strings.TrimSpace(m)
				if m != "" {
					models = append(models, m)
				}
			}
			opCfg.AIModels = models
		default:
			return fmt.Errorf("unsupported operation field: %q (expected primary_model | ai_models)", fieldName)
		}
		if opCfg.PrimaryModel == "" && len(opCfg.AIModels) == 0 {
			delete(c.Operations, opName)
		} else {
			c.Operations[opName] = opCfg
		}
	} else {
		switch key {
		case "primary_model":
			c.PrimaryModel = val
		case "ai_models":
			var models []string
			for _, m := range strings.Split(val, ",") {
				m = strings.TrimSpace(m)
				if m != "" {
					models = append(models, m)
				}
			}
			c.AIModels = models
		case "max_daily_budget_usd":
			var d float64
			if _, err := fmt.Sscanf(val, "%f", &d); err != nil {
				return fmt.Errorf("invalid float value for max_daily_budget_usd: %q", val)
			}
			c.MaxDailyBudgetUSD = d
		case "max_monthly_budget_usd":
			var d float64
			if _, err := fmt.Sscanf(val, "%f", &d); err != nil {
				return fmt.Errorf("invalid float value for max_monthly_budget_usd: %q", val)
			}
			c.MaxMonthlyBudgetUSD = d
		case "paid_tier_enabled":
			c.PaidTierEnabled = (strings.ToLower(val) == "true" || val == "1")
		case "paid_credential":
			c.PaidCredential = val
		case "paid_approval_duration_hours":
			var h int
			if _, err := fmt.Sscanf(val, "%d", &h); err != nil {
				return fmt.Errorf("invalid int value for paid_approval_duration_hours: %q", val)
			}
			c.PaidApprovalDurationHours = h
		case "max_video_duration_minutes":
			var m int
			if _, err := fmt.Sscanf(val, "%d", &m); err != nil {
				return fmt.Errorf("invalid int value for max_video_duration_minutes: %q", val)
			}
			c.MaxVideoDurationMinutes = m
		default:
			return fmt.Errorf("unsupported configuration key: %q", key)
		}
	}

	if err := config.Save(c); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	config.Reload()

	if flagJSON {
		printJSON(map[string]any{"ok": true, "key": key, "value": val})
	} else {
		fmt.Printf("Set %s to %s\n", key, val)
	}
	return nil
}

func init() {
	configExclusionsAddCmd.Flags().String("match", "domain", "Match type: domain | regex")
	configExclusionsAddCmd.Flags().String("behavior", "domain", "Behavior on match: domain | clear")

	configExclusionsCmd.AddCommand(configExclusionsListCmd)
	configExclusionsCmd.AddCommand(configExclusionsAddCmd)
	configExclusionsCmd.AddCommand(configExclusionsRemoveCmd)
	configCmd.AddCommand(configExclusionsCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configSetCmd)
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
